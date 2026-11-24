package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/mediaupload"
	"ksdevworks/ecommerce/api/internal/ent/product"
	"ksdevworks/ecommerce/api/internal/ent/productimage"
)

// Purposes of an upload intent.
const (
	PurposeProductImage = "product_image"
	PurposeContentImage = "content_image"
)

// Limits (design D6 / Decided).
const (
	MaxImagesPerProduct = 20
	MaxAltRunes         = 300
	processTimeout      = 60 * time.Second
	cacheControl        = "public, max-age=31536000, immutable"
)

// AcceptedTypes is the server-side upload whitelist. HEIC/HEIF is deliberately
// absent: every admin frontend converts to JPEG in the browser first (design D10).
var AcceptedTypes = []string{"image/jpeg", "image/png", "image/webp"}

// Errors. Each maps to one HTTP status in the handler.
var (
	ErrNotFound = errors.New("media: not found") // 404
)

// ValidationError → 422.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// ConflictError → 409.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

func invalid(format string, a ...any) error { return &ValidationError{Message: fmt.Sprintf(format, a...)} }

// Service owns upload intents and product images. It runs outside tenant
// context (admin paths), so every query carries shop_id explicitly.
type Service struct {
	Client *ent.Client
	// Store is NullStore when media is disabled (never nil, see NullStore).
	Store ObjectStore
	Cfg   config.MediaConfig
	Log   *slog.Logger

	semOnce sync.Once
	sem     chan struct{}
}

// Enabled reports whether media storage is configured.
func (s *Service) Enabled() bool { return s.Cfg.Enabled() }

// URLFor composes a public URL from a storage key (design D5: the database
// stores keys, never URLs). variant is "thumb" or "large".
func (s *Service) URLFor(imageKey, variant string) string {
	return s.Cfg.PublicBaseURL + "/" + imageKey + "/" + variant + ".webp"
}

// Urls returns the thumb/large URL pair of an image key.
func (s *Service) Urls(imageKey string) map[string]string {
	return map[string]string{"thumb": s.URLFor(imageKey, "thumb"), "large": s.URLFor(imageKey, "large")}
}

// ImageJSON is the public/admin representation of one product image.
func (s *Service) ImageJSON(im *ent.ProductImage) map[string]any {
	return map[string]any{
		"id": im.ID, "alt": im.Alt, "position": im.Position,
		"width": im.Width, "height": im.Height, "urls": s.Urls(im.ImageKey),
	}
}

// ImagesJSON renders a product's loaded images; never nil (design D9).
func (s *Service) ImagesJSON(images []*ent.ProductImage) []map[string]any {
	out := make([]map[string]any, 0, len(images))
	if !s.Enabled() {
		return out // no public domain to build URLs from: report none (spec: media disabled)
	}
	for _, im := range images {
		out = append(out, s.ImageJSON(im))
	}
	return out
}

// WarmUp initialises the WASM WebP encoder; call once at boot when enabled.
func (s *Service) WarmUp() error {
	if !s.Enabled() {
		return nil
	}
	return WarmUp()
}

func (s *Service) acquire(ctx context.Context) (release func(), err error) {
	s.semOnce.Do(func() {
		n := s.Cfg.ProcessConcurrency
		if n < 1 {
			n = 1
		}
		s.sem = make(chan struct{}, n)
	})
	select {
	case s.sem <- struct{}{}:
		return func() { <-s.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ── upload intents ───────────────────────────────────────────────────

// UploadInput is the declared upload.
type UploadInput struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Purpose     string `json:"purpose"`
}

// UploadResult is a created intent plus its presigned PUT.
type UploadResult struct {
	UploadID  uuid.UUID
	UploadURL string
	Headers   map[string]string
	ExpiresAt time.Time
}

// ValidateUpload checks the declared upload without side effects.
func (s *Service) ValidateUpload(in UploadInput) error {
	switch in.Purpose {
	case PurposeProductImage, PurposeContentImage:
	default:
		return invalid("purpose must be %q or %q", PurposeProductImage, PurposeContentImage)
	}
	ct := strings.ToLower(strings.TrimSpace(in.ContentType))
	if ct == "image/heic" || ct == "image/heif" || ct == "image/heic-sequence" || ct == "image/heif-sequence" {
		return invalid("HEIC/HEIF is not accepted by the server: convert the photo to JPEG or WebP before uploading")
	}
	ok := false
	for _, a := range AcceptedTypes {
		if ct == a {
			ok = true
		}
	}
	if !ok {
		return invalid("content_type must be one of %s", strings.Join(AcceptedTypes, ", "))
	}
	if in.SizeBytes <= 0 {
		return invalid("size_bytes must be positive")
	}
	if in.SizeBytes > s.Cfg.MaxBytes {
		return invalid("size_bytes exceeds the %d byte limit", s.Cfg.MaxBytes)
	}
	return nil
}

// CreateUpload records an upload intent and presigns a PUT for the object key
// the API itself minted (design D3).
func (s *Service) CreateUpload(ctx context.Context, shopID, userID int, in UploadInput) (*UploadResult, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if err := s.ValidateUpload(in); err != nil {
		return nil, err
	}
	ct := strings.ToLower(strings.TrimSpace(in.ContentType))
	id := uuid.New()
	key := fmt.Sprintf("incoming/%d/%s", shopID, id)
	exp := time.Now().Add(s.Cfg.UploadURLTTL)

	put, err := s.Store.PresignPut(ctx, key, ct, in.SizeBytes, s.Cfg.UploadURLTTL)
	if err != nil {
		return nil, err
	}
	if _, err := s.Client.MediaUpload.Create().
		SetID(id).SetShopID(shopID).SetObjectKey(key).SetContentType(ct).
		SetSizeBytes(in.SizeBytes).SetPurpose(in.Purpose).SetCreatedBy(userID).
		SetExpiresAt(exp).Save(ctx); err != nil {
		return nil, fmt.Errorf("media: create upload: %w", err)
	}
	return &UploadResult{UploadID: id, UploadURL: put.URL, Headers: put.Headers, ExpiresAt: exp}, nil
}

// consume atomically claims an intent (design D3): one conditional UPDATE whose
// affected-row count is the arbiter, so two concurrent attaches of the same
// upload_id cannot both win. Lookup failures of every kind (unknown id, other
// shop, wrong purpose, expired) are one 422 "treat as non-existent"; only a
// genuinely already-consumed intent is a 409.
func (s *Service) consume(ctx context.Context, shopID int, uploadID uuid.UUID, purpose string) (*ent.MediaUpload, error) {
	now := time.Now()
	n, err := s.Client.MediaUpload.Update().
		Where(mediaupload.ID(uploadID), mediaupload.ShopID(shopID), mediaupload.Purpose(purpose),
			mediaupload.ConsumedAtIsNil(), mediaupload.ExpiresAtGT(now)).
		SetConsumedAt(now).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("media: consume upload: %w", err)
	}
	if n == 1 {
		return s.Client.MediaUpload.Query().
			Where(mediaupload.ID(uploadID), mediaupload.ShopID(shopID)).Only(ctx)
	}
	up, err := s.Client.MediaUpload.Query().
		Where(mediaupload.ID(uploadID), mediaupload.ShopID(shopID), mediaupload.Purpose(purpose)).Only(ctx)
	if err != nil || !up.ExpiresAt.After(now) {
		return nil, invalid("upload_id is unknown, expired or not valid for this operation")
	}
	return nil, &ConflictError{Message: "upload_id has already been used"}
}

// release un-claims an intent after a failed attempt so the client can retry
// without PUTting the file again (design Risks: failure keeps the intent).
func (s *Service) release(uploadID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Client.MediaUpload.UpdateOneID(uploadID).ClearConsumedAt().Exec(ctx); err != nil {
		s.Log.Warn("media: release upload intent failed", "upload_id", uploadID.String(), "err", err)
	}
}

// processUpload reads the PUT object back, validates and re-encodes it, then
// writes both variants under keyPrefix. On success the original is NOT yet
// deleted (callers delete it after their DB commit).
func (s *Service) processUpload(ctx context.Context, up *ent.MediaUpload, keyPrefix string) ([]Variant, error) {
	raw, err := s.Store.Get(ctx, up.ObjectKey, s.Cfg.MaxBytes)
	switch {
	case errors.Is(err, ErrObjectNotFound):
		return nil, invalid("the file has not been uploaded to the presigned URL")
	case errors.Is(err, ErrObjectTooLarge):
		return nil, invalid("uploaded file exceeds the %d byte limit", s.Cfg.MaxBytes)
	case err != nil:
		return nil, err
	}

	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	pctx, cancel := context.WithTimeout(ctx, processTimeout)
	defer cancel()
	type result struct {
		v   []Variant
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := Process(raw, ProcessOptions{MaxPixels: s.Cfg.MaxPixels, Quality: s.Cfg.WebPQuality})
		done <- result{v, err}
	}()
	var variants []Variant
	select {
	case r := <-done:
		if r.err != nil {
			switch {
			case errors.Is(r.err, ErrUnsupportedImage):
				return nil, invalid("file content is not a JPEG, PNG or WebP image")
			case errors.Is(r.err, ErrTooManyPixels):
				return nil, invalid("image exceeds the %d pixel limit", s.Cfg.MaxPixels)
			case errors.Is(r.err, ErrCorruptImage):
				return nil, invalid("image could not be decoded")
			}
			return nil, r.err
		}
		variants = r.v
	case <-pctx.Done():
		return nil, fmt.Errorf("media: processing timed out: %w", pctx.Err())
	}

	var written []string
	for _, v := range variants {
		k := keyPrefix + "/" + v.Name + ".webp"
		if err := s.Store.Put(ctx, k, v.Data, "image/webp", cacheControl); err != nil {
			s.deleteQuiet(written, up.ShopID)
			return nil, err
		}
		written = append(written, k)
	}
	return variants, nil
}

func variantByName(vs []Variant, name string) Variant {
	for _, v := range vs {
		if v.Name == name {
			return v
		}
	}
	return Variant{}
}

// deleteQuiet is the post-commit / cleanup deletion of design D8: failure
// leaves an orphan object (cost, not correctness), so it only logs.
func (s *Service) deleteQuiet(keys []string, shopID int) {
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Store.Delete(ctx, keys...); err != nil {
		s.Log.Warn("media: object delete failed (orphan left behind)", "shop_id", shopID, "keys", keys, "err", err)
	}
}

func validateAlt(alt string) error {
	if utf8.RuneCountInString(alt) > MaxAltRunes {
		return invalid("alt must be at most %d characters", MaxAltRunes)
	}
	return nil
}

// ── product images ───────────────────────────────────────────────────

func (s *Service) requireProduct(ctx context.Context, shopID, productID int) error {
	ok, err := s.Client.Product.Query().Where(product.ID(productID), product.ShopID(shopID)).Exist(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// ListImages returns a product's images ordered by (position, id).
func (s *Service) ListImages(ctx context.Context, shopID, productID int) ([]*ent.ProductImage, error) {
	if err := s.requireProduct(ctx, shopID, productID); err != nil {
		return nil, err
	}
	return s.Client.ProductImage.Query().
		Where(productimage.ShopID(shopID), productimage.ProductID(productID)).
		Order(productimage.ByPosition(), productimage.ByID()).All(ctx)
}

// AttachProductImage turns an uploaded original into two WebP variants and a
// product_images row. It answers 201 only after the variants are readable.
func (s *Service) AttachProductImage(ctx context.Context, shopID, productID int, uploadID uuid.UUID, alt string) (*ent.ProductImage, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if err := validateAlt(alt); err != nil {
		return nil, err
	}
	if err := s.requireProduct(ctx, shopID, productID); err != nil {
		return nil, err
	}
	// Cheap early check; the authoritative one is inside the insert transaction.
	if n, err := s.Client.ProductImage.Query().Where(productimage.ShopID(shopID), productimage.ProductID(productID)).Count(ctx); err != nil {
		return nil, err
	} else if n >= MaxImagesPerProduct {
		return nil, &ConflictError{Message: fmt.Sprintf("a product can have at most %d images", MaxImagesPerProduct)}
	}

	up, err := s.consume(ctx, shopID, uploadID, PurposeProductImage)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			s.release(uploadID)
		}
	}()

	imageKey := fmt.Sprintf("shops/%d/products/%d/%s", shopID, productID, uuid.New())
	variants, err := s.processUpload(ctx, up, imageKey)
	if err != nil {
		return nil, err
	}
	keys := []string{imageKey + "/thumb.webp", imageKey + "/large.webp"}
	large := variantByName(variants, "large")

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		s.deleteQuiet(keys, shopID)
		return nil, err
	}
	rollback := func(e error) (*ent.ProductImage, error) {
		_ = tx.Rollback()
		s.deleteQuiet(keys, shopID)
		return nil, e
	}
	// Serialise concurrent attaches for this product so the limit and the
	// next position are decided against a stable view.
	if _, err := tx.Product.Query().Where(product.ID(productID), product.ShopID(shopID)).ForUpdate().Only(ctx); err != nil {
		if ent.IsNotFound(err) {
			return rollback(ErrNotFound)
		}
		return rollback(err)
	}
	existing, err := tx.ProductImage.Query().Where(productimage.ShopID(shopID), productimage.ProductID(productID)).All(ctx)
	if err != nil {
		return rollback(err)
	}
	if len(existing) >= MaxImagesPerProduct {
		return rollback(&ConflictError{Message: fmt.Sprintf("a product can have at most %d images", MaxImagesPerProduct)})
	}
	pos := int32(0)
	for _, e := range existing {
		if e.Position >= pos {
			pos = e.Position + 1
		}
	}
	im, err := tx.ProductImage.Create().
		SetShopID(shopID).SetProductID(productID).SetImageKey(imageKey).
		SetPosition(pos).SetAlt(alt).SetWidth(int32(large.Width)).SetHeight(int32(large.Height)).
		Save(ctx)
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		s.deleteQuiet(keys, shopID)
		return nil, err
	}
	ok = true
	s.deleteQuiet([]string{up.ObjectKey}, shopID) // original is no longer needed (design D4)
	return im, nil
}

// ContentImage is the result of a CMS content image upload.
type ContentImage struct {
	Urls          map[string]string
	Width, Height int
}

// CreateContentImage runs the same pipeline for CMS image fields; there is no
// database row, the returned URL is what the editor stores in page JSON.
func (s *Service) CreateContentImage(ctx context.Context, shopID int, uploadID uuid.UUID) (*ContentImage, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	up, err := s.consume(ctx, shopID, uploadID, PurposeContentImage)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			s.release(uploadID)
		}
	}()
	imageKey := fmt.Sprintf("shops/%d/content/%s", shopID, uuid.New())
	variants, err := s.processUpload(ctx, up, imageKey)
	if err != nil {
		return nil, err
	}
	ok = true
	s.deleteQuiet([]string{up.ObjectKey}, shopID)
	large := variantByName(variants, "large")
	return &ContentImage{Urls: s.Urls(imageKey), Width: large.Width, Height: large.Height}, nil
}

// UpdateAlt changes only the alt text.
func (s *Service) UpdateAlt(ctx context.Context, shopID, productID, imageID int, alt string) (*ent.ProductImage, error) {
	if err := validateAlt(alt); err != nil {
		return nil, err
	}
	n, err := s.Client.ProductImage.Update().
		Where(productimage.ID(imageID), productimage.ShopID(shopID), productimage.ProductID(productID)).
		SetAlt(alt).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return s.Client.ProductImage.Query().Where(productimage.ID(imageID), productimage.ShopID(shopID)).Only(ctx)
}

// Reorder applies a complete permutation of the product's images in one
// transaction. Partial orders are rejected (design D6): two tabs reordering at
// once must not silently leave duplicate positions.
func (s *Service) Reorder(ctx context.Context, shopID, productID int, ids []int) ([]*ent.ProductImage, error) {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(e error) ([]*ent.ProductImage, error) {
		_ = tx.Rollback()
		return nil, e
	}
	if _, err := tx.Product.Query().Where(product.ID(productID), product.ShopID(shopID)).ForUpdate().Only(ctx); err != nil {
		if ent.IsNotFound(err) {
			return fail(ErrNotFound)
		}
		return fail(err)
	}
	cur, err := tx.ProductImage.Query().Where(productimage.ShopID(shopID), productimage.ProductID(productID)).All(ctx)
	if err != nil {
		return fail(err)
	}
	have := make(map[int]bool, len(cur))
	for _, c := range cur {
		have[c.ID] = true
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if !have[id] || seen[id] {
			return fail(invalid("image_ids must be exactly the product's current images, each once"))
		}
		seen[id] = true
	}
	if len(ids) != len(cur) {
		return fail(invalid("image_ids must be exactly the product's current images, each once"))
	}
	for i, id := range ids {
		if err := tx.ProductImage.UpdateOneID(id).SetPosition(int32(i)).Exec(ctx); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListImages(ctx, shopID, productID)
}

// DeleteImage removes the row in a transaction and the objects after commit.
func (s *Service) DeleteImage(ctx context.Context, shopID, productID, imageID int) error {
	im, err := s.Client.ProductImage.Query().
		Where(productimage.ID(imageID), productimage.ShopID(shopID), productimage.ProductID(productID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	n, err := s.Client.ProductImage.Delete().Where(productimage.ID(im.ID), productimage.ShopID(shopID)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 1 && s.Enabled() {
		s.deleteQuiet([]string{im.ImageKey + "/thumb.webp", im.ImageKey + "/large.webp"}, shopID)
	}
	return nil
}

// DeleteProductObjects removes every stored object of a deleted product
// (best-effort, after the product's own deletion committed; design D8). The
// rows are already gone via ON DELETE CASCADE.
func (s *Service) DeleteProductObjects(ctx context.Context, shopID, productID int) {
	if !s.Enabled() {
		return
	}
	prefix := fmt.Sprintf("shops/%d/products/%d/", shopID, productID)
	keys, err := s.Store.List(ctx, prefix)
	if err != nil {
		s.Log.Warn("media: list objects of deleted product failed (orphans left behind)", "shop_id", shopID, "product_id", productID, "err", err)
		return
	}
	s.deleteQuiet(keys, shopID)
}

// NewService builds the service. With media disabled the store is NullStore, so
// the Service (and every route that uses it) always exists and answers 503
// instead of branching on nil (design D2); when enabled, store must be non-nil.
func NewService(client *ent.Client, cfg config.MediaConfig, store ObjectStore, log *slog.Logger) *Service {
	if !cfg.Enabled() || store == nil {
		store = NullStore{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{Client: client, Store: store, Cfg: cfg, Log: log}
}
