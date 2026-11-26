package design

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"image"
	"image/png"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designrequest"
	"ksdevworks/ecommerce/api/internal/ent/designscreenshot"
	"ksdevworks/ecommerce/api/internal/media"
)

// Screenshot storage (change design-history, slice 2; design D5, D6, D11).
//
// The bucket is private and separate from the public image bucket. The only
// way to read an object is a short-lived signed URL minted by ListScreenshots;
// the URL is never logged and never stored.

// Limits (design D5).
const (
	MaxScreenshotBytes    = 2 << 20  // per image, uploaded and stored
	MaxScreenshotsPerReq  = 80       // retained slots per request (a re-shoot of a slot counts once)
	// DefaultMaxScreenshotAttempts bounds every upload attempt per request
	// (3x the slot cap); DESIGN_MAX_SCREENSHOT_ATTEMPTS overrides it.
	DefaultMaxScreenshotAttempts = 3 * MaxScreenshotsPerReq
	MaxScreenshotBytesReq = 40 << 20 // bytes per request
	MaxScreenshotHeight   = 10_000
	MaxManifestBytes      = 2 << 10
	maxScreenshotPixels   = 1280 * MaxScreenshotHeight

	DatasetSandbox     = "sandbox-v1"
	DatasetPublicLive  = "public-live"
	OriginClassSandbox = "sandbox"
	OriginClassPublic  = "public"

	defaultProcessSlots    = 2
	screenshotCacheControl = "private, no-store"
)

// ObjectStore is the part of media.ObjectStore the screenshots need. The
// implementation is a media.S3Store pointed at the private design bucket, so
// the S3/R2 client code is shared while bucket, keys and config stay separate.
type ObjectStore interface {
	Put(ctx context.Context, key string, body []byte, contentType, cacheControl string) error
	Delete(ctx context.Context, keys ...string) error
	List(ctx context.Context, prefix string) ([]string, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// media.ObjectStore must stay a superset, so a media store can back this one.
var _ ObjectStore = media.ObjectStore(nil)

var viewKeyRE = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

func invalidShot(msg string) *Error {
	return newErr(http.StatusUnprocessableEntity, "invalid_screenshot", msg)
}

// ScreenshotKey is the object key of a screenshot: shops/{shop}/design/{request}/{id}.png.
func ScreenshotKey(shopID int, requestID, id uuid.UUID) string {
	return fmt.Sprintf("shops/%d/design/%s/%s.png", shopID, requestID, id)
}

// Manifest is the validated capture_manifest. Only these fields are stored;
// anything else the service sent is dropped.
type Manifest struct {
	Tool        string `json:"tool"`
	ToolVersion string `json:"tool_version"`
	Dataset     string `json:"dataset"`
	APIOrigin   string `json:"api_origin_class"`
	CapturedAt  string `json:"captured_at"`
	SetVersion  int    `json:"set_version"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// ParseManifest decodes the base64 JSON of X-Capture-Manifest and checks its
// shape. It does not apply the sandbox rule (that depends on the site).
func ParseManifest(b64 string) (Manifest, error) {
	var m Manifest
	if b64 == "" {
		return m, invalidShot("X-Capture-Manifest is required")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return m, invalidShot("X-Capture-Manifest must be base64 of a JSON object")
	}
	if len(raw) > MaxManifestBytes {
		return m, invalidShot("the capture manifest is larger than 2 KiB")
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, invalidShot("the capture manifest is not a valid JSON object")
	}
	switch {
	case m.Tool == "" || len(m.Tool) > 60 || len(m.ToolVersion) > 40:
		return m, invalidShot("the capture manifest needs tool (<= 60 chars) and tool_version (<= 40 chars)")
	case m.SetVersion < 1 || m.SetVersion > 1000:
		return m, invalidShot("the capture manifest needs set_version >= 1")
	case m.APIOrigin != OriginClassSandbox && m.APIOrigin != OriginClassPublic:
		return m, invalidShot("api_origin_class must be sandbox or public")
	}
	if _, err := time.Parse(time.RFC3339, m.CapturedAt); err != nil {
		return m, invalidShot("captured_at must be an RFC 3339 time")
	}
	return m, nil
}

// checkCaptureRules is the privacy rule R1/R2 at the door (design D11, Q5).
// The platform cannot prove where a screenshot came from; it can refuse one
// that declares it came from live data where that is not allowed.
func checkCaptureRules(site, role string, m Manifest) error {
	refuse := func(msg string) error {
		return newErr(http.StatusUnprocessableEntity, "live_data_capture_refused", msg)
	}
	switch site {
	case SiteAdmin:
		// Admin screens show customers and orders: only a sandbox shop with the
		// fixed synthetic dataset may be captured, never a merchant's live admin.
		if m.Dataset != DatasetSandbox || m.APIOrigin != OriginClassSandbox {
			return refuse("admin screenshots must be captured from the sandbox stack (dataset sandbox-v1)")
		}
	case SiteStorefront:
		// Storefront shots are public data only: no login, so only the anonymous role.
		if role != "anon" {
			return refuse("storefront screenshots are captured anonymously (role anon)")
		}
		switch {
		case m.Dataset == DatasetSandbox && m.APIOrigin == OriginClassSandbox:
		case m.Dataset == DatasetPublicLive && m.APIOrigin == OriginClassPublic:
		default:
			return refuse("storefront screenshots must declare dataset sandbox-v1 on a sandbox origin, or public-live on a public origin")
		}
	}
	return nil
}

// UploadInput is one screenshot PUT.
type UploadInput struct {
	ShopID    int
	RequestID uuid.UUID
	Phase     string // before | after
	Role      string // owner | staff | anon
	Viewport  int
	ViewKey   string
	Body      []byte // the uploaded bytes (already size-limited by the caller)
	// ClaimedSHA256 is the optional X-Content-SHA256: it must equal the hash of Body.
	ClaimedSHA256 string
	Manifest      string // X-Capture-Manifest, base64 JSON
	DiffPPM       *int
}

// reencode validates the PNG against the viewport and re-encodes it. Go's
// encoder writes no ancillary chunks, so tEXt/eXIf/iCCP metadata disappears.
func (s *Service) reencode(ctx context.Context, body []byte, viewport int) (out []byte, w, h int, err error) {
	cfg, format, derr := image.DecodeConfig(bytes.NewReader(body))
	if derr != nil || format != "png" {
		return nil, 0, 0, invalidShot("the body is not a PNG image")
	}
	switch {
	case cfg.Width != viewport:
		return nil, 0, 0, invalidShot(fmt.Sprintf("the image width must equal the viewport (%d)", viewport))
	case cfg.Height < 1 || cfg.Height > MaxScreenshotHeight:
		return nil, 0, 0, invalidShot(fmt.Sprintf("the image height must be between 1 and %d", MaxScreenshotHeight))
	case cfg.Width*cfg.Height > maxScreenshotPixels:
		return nil, 0, 0, invalidShot("the image has too many pixels")
	}
	release, aerr := s.acquire(ctx)
	if aerr != nil {
		return nil, 0, 0, aerr
	}
	defer release()
	img, derr := png.Decode(bytes.NewReader(body))
	if derr != nil {
		return nil, 0, 0, invalidShot("the PNG cannot be decoded")
	}
	var buf bytes.Buffer
	if eerr := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img); eerr != nil {
		return nil, 0, 0, eerr
	}
	if buf.Len() > MaxScreenshotBytes {
		return nil, 0, 0, invalidShot("the re-encoded image is larger than 2 MiB")
	}
	return buf.Bytes(), cfg.Width, cfg.Height, nil
}

// acquire takes one decode slot (the same idea as media's process semaphore: a
// 1280x10000 decode is tens of MiB, so only a few run at once).
func (s *Service) acquire(ctx context.Context) (func(), error) {
	s.semOnce.Do(func() {
		n := s.Cfg.ProcessConcurrency
		if n < 1 {
			n = defaultProcessSlots
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

// storageOn reports whether screenshot storage is configured. A disabled
// deployment carries a media.NullStore (never a nil, so the wiring audit can
// insist that nothing is left unset), hence the explicit flag.
func (s *Service) storageOn() bool { return s.Store != nil && s.Cfg.ScreenshotsEnabled }

func errStorageNotConfigured() *Error {
	return newErr(http.StatusServiceUnavailable, "design_storage_not_configured", "screenshot storage is not configured on this server")
}

// UploadScreenshot validates and stores one screenshot. The bool is true when
// the same bytes were already stored for the slot (a replay: nothing is added).
func (s *Service) UploadScreenshot(ctx context.Context, in UploadInput) (*ent.DesignScreenshot, bool, error) {
	// 404 first (authorization already happened in the credential middleware),
	// then the storage check, so a caller learns nothing about the deployment
	// for a request it cannot see.
	req, err := s.load(ctx, in.ShopID, in.RequestID)
	if err != nil {
		return nil, false, err
	}
	if !s.storageOn() {
		return nil, false, errStorageNotConfigured()
	}
	if in.Phase != "before" && in.Phase != "after" {
		return nil, false, ErrInvalid("phase must be before or after")
	}
	if in.Role != "owner" && in.Role != "staff" && in.Role != "anon" {
		return nil, false, ErrInvalid("role must be owner, staff or anon")
	}
	if in.Viewport != 390 && in.Viewport != 1280 {
		return nil, false, ErrInvalid("viewport must be 390 or 1280")
	}
	if !viewKeyRE.MatchString(in.ViewKey) {
		return nil, false, ErrInvalid("view_key must match ^[a-z0-9-]{1,40}$")
	}
	if req.Status != StatusDrafting && req.Status != StatusPreviewing {
		return nil, false, ErrInvalidState("screenshots can only be uploaded while the request is in drafting or previewing")
	}
	if len(in.Body) == 0 || len(in.Body) > MaxScreenshotBytes {
		return nil, false, invalidShot("the image must be between 1 byte and 2 MiB")
	}
	if in.ClaimedSHA256 != "" {
		sum := sha256.Sum256(in.Body)
		if !strings.EqualFold(in.ClaimedSHA256, hex.EncodeToString(sum[:])) {
			return nil, false, invalidShot("X-Content-SHA256 does not match the uploaded bytes")
		}
	}
	if in.DiffPPM != nil && (*in.DiffPPM < 0 || *in.DiffPPM > 1_000_000) {
		return nil, false, invalidShot("diff_ppm must be between 0 and 1000000")
	}
	man, err := ParseManifest(in.Manifest)
	if err != nil {
		return nil, false, err
	}
	if err := checkCaptureRules(req.Site, in.Role, man); err != nil {
		return nil, false, err
	}
	// before = the base commit, after = the current head. A head that has not
	// been reported yet cannot have an after-screenshot.
	commit := req.BaseSha
	if in.Phase == "after" {
		commit = req.HeadSha
	}
	if len(commit) != 40 {
		return nil, false, conflict("head_not_set", "report the head commit before uploading after screenshots")
	}
	stored, w, h, err := s.reencode(ctx, in.Body, in.Viewport)
	if err != nil {
		return nil, false, err
	}
	sum := sha256.Sum256(stored)
	storedHash := hex.EncodeToString(sum[:])
	manifest, _ := json.Marshal(man)

	var out *ent.DesignScreenshot
	replay := false
	err = s.inTx(ctx, func(tx *ent.Tx) error {
		// The request row lock serializes uploads of one request (attempt
		// allocation and the per-request caps).
		locked, err := tx.DesignRequest.Query().Where(designrequest.IDEQ(in.RequestID), designrequest.ShopIDEQ(in.ShopID)).ForUpdate().Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return ErrNotFound()
			}
			return err
		}
		if locked.Status != StatusDrafting && locked.Status != StatusPreviewing {
			return ErrInvalidState("screenshots can only be uploaded while the request is in drafting or previewing")
		}
		if locked.HeadSha != req.HeadSha {
			return conflict("head_moved", "the head changed during the upload; capture again")
		}
		last, err := tx.DesignScreenshot.Query().
			Where(designscreenshot.RequestIDEQ(in.RequestID), designscreenshot.PhaseEQ(in.Phase), designscreenshot.RoleEQ(in.Role),
				designscreenshot.ViewportEQ(viewportInt16(in.Viewport)), designscreenshot.ViewKeyEQ(in.ViewKey)).
			Order(ent.Desc(designscreenshot.FieldAttempt)).First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return err
		}
		if last != nil && last.Sha256 == storedHash && last.CommitSha == commit && last.PurgedAt == nil {
			out, replay = last, true // same bytes, same commit: nothing to add
			return nil
		}
		attempt := int16(1)
		if last != nil {
			attempt = last.Attempt + 1
		}
		// Per-request caps. Slot cap: distinct slots (a re-shoot supersedes the
		// previous attempt and counts once). Attempt cap: every attempt ever
		// stored for the request (a wider abuse bound). Byte cap: attempts that
		// still hold an object; superseded attempts stay counted until purged,
		// exactly as the per-shop quota below counts them.
		prior, err := tx.DesignScreenshot.Query().Where(designscreenshot.RequestIDEQ(in.RequestID)).All(ctx)
		if err != nil {
			return err
		}
		type slotKey struct {
			phase, role, view string
			viewport          int16
		}
		slots := map[slotKey]bool{}
		reqBytes := len(stored)
		for _, r := range prior {
			slots[slotKey{r.Phase, r.Role, r.ViewKey, r.Viewport}] = true
			if r.PurgedAt == nil {
				reqBytes += r.ByteSize
			}
		}
		attemptCap := s.Cfg.MaxScreenshotAttempts
		if attemptCap <= 0 {
			attemptCap = DefaultMaxScreenshotAttempts
		}
		if len(prior)+1 > attemptCap {
			return conflict("attempt_cap_exceeded", fmt.Sprintf("this request has reached its upload attempt limit (%d attempts)", attemptCap))
		}
		if last == nil && len(slots)+1 > MaxScreenshotsPerReq {
			return conflict("slot_cap_exceeded", fmt.Sprintf("this request has reached its screenshot slot limit (%d slots); re-shooting an existing slot is still allowed", MaxScreenshotsPerReq))
		}
		if reqBytes > MaxScreenshotBytesReq {
			return conflict("quota_exceeded", "this request has reached its screenshot size limit (40 MiB)")
		}
		shopSizes, err := tx.DesignScreenshot.Query().Where(designscreenshot.ShopIDEQ(in.ShopID), designscreenshot.PurgedAtIsNil()).
			Select(designscreenshot.FieldByteSize).Ints(ctx)
		if err != nil {
			return err
		}
		shopBytes := int64(len(stored))
		for _, b := range shopSizes {
			shopBytes += int64(b)
		}
		if quota := s.Cfg.ShopQuotaBytes; quota > 0 && shopBytes > quota {
			return conflict("quota_exceeded", "this shop has reached its design screenshot storage quota")
		}
		id := uuid.New()
		key := ScreenshotKey(in.ShopID, in.RequestID, id)
		if err := s.Store.Put(ctx, key, stored, "image/png", screenshotCacheControl); err != nil {
			s.Log.Error("design screenshot: store put failed", "shop_id", in.ShopID, "err_class", "storage")
			return newErr(http.StatusBadGateway, "storage_unavailable", "the screenshot could not be stored; retry")
		}
		create := tx.DesignScreenshot.Create().
			SetID(id).SetShopID(in.ShopID).SetRequestID(in.RequestID).SetPhase(in.Phase).SetSite(req.Site).
			SetViewKey(in.ViewKey).SetViewport(viewportInt16(in.Viewport)).SetRole(in.Role).SetAttempt(attempt).
			SetCommitSha(commit).SetObjectKey(key).SetContentType("image/png").SetByteSize(len(stored)).
			SetWidth(w).SetHeight(h).SetSha256(storedHash).SetCaptureManifest(manifest)
		if in.DiffPPM != nil {
			create.SetDiffPpm(*in.DiffPPM)
		}
		row, err := create.Save(ctx)
		if err != nil {
			// The object is unreferenced now; remove it best-effort (the sweep
			// reports any that remain as orphans).
			_ = s.Store.Delete(ctx, key)
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, replay, nil
}

// ScreenshotView is a stored screenshot with a signed read URL.
type ScreenshotView struct {
	Row          *ent.DesignScreenshot
	URL          string // empty when the object was purged
	URLExpiresAt time.Time
}

// ListScreenshots returns the request's screenshots with signed URLs (TTL from
// config, default 5 minutes). By default only the newest attempt per slot;
// all=true returns every attempt. phase filters when non-empty. A missing store
// answers 503 after the request check (the caller is already authorised).
func (s *Service) ListScreenshots(ctx context.Context, shopID int, id uuid.UUID, phase string, all bool) ([]ScreenshotView, error) {
	if _, err := s.load(ctx, shopID, id); err != nil {
		return nil, err
	}
	if !s.storageOn() {
		return nil, errStorageNotConfigured()
	}
	if phase != "" && phase != "before" && phase != "after" {
		return nil, ErrInvalid("phase must be before or after")
	}
	q := s.Client.DesignScreenshot.Query().Where(designscreenshot.RequestIDEQ(id), designscreenshot.ShopIDEQ(shopID))
	if phase != "" {
		q = q.Where(designscreenshot.PhaseEQ(phase))
	}
	rows, err := q.Order(ent.Asc(designscreenshot.FieldPhase), ent.Asc(designscreenshot.FieldRole), ent.Asc(designscreenshot.FieldViewport),
		ent.Asc(designscreenshot.FieldViewKey), ent.Asc(designscreenshot.FieldAttempt)).All(ctx)
	if err != nil {
		return nil, err
	}
	if !all { // attempts ascend within a slot, so the last of each slot wins
		type key struct {
			phase, role, view string
			viewport          int16
		}
		latest := map[key]*ent.DesignScreenshot{}
		var order []key
		for _, r := range rows {
			k := key{r.Phase, r.Role, r.ViewKey, r.Viewport}
			if _, ok := latest[k]; !ok {
				order = append(order, k)
			}
			latest[k] = r
		}
		rows = rows[:0]
		for _, k := range order {
			rows = append(rows, latest[k])
		}
	}
	ttl := s.Cfg.ScreenshotURLTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	exp := s.now().Add(ttl)
	out := make([]ScreenshotView, 0, len(rows))
	for _, r := range rows {
		v := ScreenshotView{Row: r}
		if r.PurgedAt == nil {
			u, err := s.Store.PresignGet(ctx, r.ObjectKey, ttl)
			if err != nil {
				return nil, fmt.Errorf("design: presign screenshot: %w", err)
			}
			v.URL, v.URLExpiresAt = u, exp
		}
		out = append(out, v)
	}
	return out, nil
}

type slot struct {
	Role     string
	Viewport int16
	View     string
}

// screenshotsComplete requires the before set (taken at base_sha) and the after
// set (taken at head_sha) to exist, to cover exactly the same slots, and to come
// from the same view-set version.
func (s *Service) screenshotsComplete(ctx context.Context, r *ent.DesignRequest) error {
	rows, err := s.Client.DesignScreenshot.Query().
		Where(designscreenshot.RequestIDEQ(r.ID), designscreenshot.ShopIDEQ(r.ShopID), designscreenshot.PurgedAtIsNil()).
		Order(ent.Asc(designscreenshot.FieldAttempt)).All(ctx)
	if err != nil {
		return err
	}
	latest := map[string]map[slot]*ent.DesignScreenshot{"before": {}, "after": {}}
	for _, row := range rows { // attempts ascend, so the last write per slot is the newest
		latest[row.Phase][slot{row.Role, row.Viewport, row.ViewKey}] = row
	}
	incomplete := func(why string) error {
		return conflict("screenshots_incomplete", why)
	}
	if len(latest["before"]) == 0 || len(latest["after"]) == 0 {
		return incomplete("both the before and the after screenshot sets are required")
	}
	if len(latest["before"]) != len(latest["after"]) {
		return incomplete("the before and after sets cover different views")
	}
	versions := map[int]bool{}
	for sl, b := range latest["before"] {
		a, ok := latest["after"][sl]
		if !ok {
			return incomplete(fmt.Sprintf("no after screenshot for %s/%d/%s", sl.Role, sl.Viewport, sl.View))
		}
		if b.CommitSha != r.BaseSha {
			return incomplete("a before screenshot was not taken at the base commit")
		}
		if a.CommitSha != r.HeadSha {
			return incomplete("an after screenshot was not taken at the current head")
		}
		versions[setVersion(b.CaptureManifest)] = true
		versions[setVersion(a.CaptureManifest)] = true
	}
	if len(versions) != 1 {
		vs := make([]int, 0, len(versions))
		for v := range versions {
			vs = append(vs, v)
		}
		sort.Ints(vs)
		return incomplete(fmt.Sprintf("the screenshots come from different view-set versions %v", vs))
	}
	return nil
}

func setVersion(manifest json.RawMessage) int {
	var m struct {
		SetVersion int `json:"set_version"`
	}
	_ = json.Unmarshal(manifest, &m)
	return m.SetVersion
}

// viewportInt16 narrows a viewport width for the int16 column. Upload already
// restricts it to 390 or 1280; the range check makes the conversion safe on
// its own and returns 0 (matches no stored row) for anything out of range.
func viewportInt16(v int) int16 {
	if v < math.MinInt16 || v > math.MaxInt16 {
		return 0
	}
	return int16(v)
}
