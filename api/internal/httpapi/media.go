package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/media"
)

// MediaHandler serves upload intents, CMS content images and the product image
// sub-resource (change product-images-r2).
//
// Every route is mounted whether or not media storage is configured (design D2,
// wiring check 6): when it is not, each answers 503 media_not_configured. The
// check runs AFTER authentication/authorisation, so an unauthorised caller
// still gets 401/403 and never learns the deployment's configuration.
type MediaHandler struct {
	Client  *ent.Client
	Service *media.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountShopAdmin: routes under /admin/shops/{shopID}.
func (h *MediaHandler) MountShopAdmin(r chi.Router) {
	// The permission for an upload intent depends on its purpose, which lives in
	// the body, so the route only requires shop access and the handler decides.
	r.With(h.Authz.RequireShopAccess()).Post("/media/uploads", h.createUpload)
	r.With(h.Authz.RequireShopPermission("page.edit")).Post("/media/content-images", h.createContentImage)

	r.With(h.Authz.RequireShopPermission("product.view")).Get("/products/{productID}/images", h.listImages)
	r.With(h.Authz.RequireShopPermission("product.edit")).Post("/products/{productID}/images", h.attachImage)
	// "order" is a static segment and chi matches static before {imageID}.
	r.With(h.Authz.RequireShopPermission("product.edit")).Put("/products/{productID}/images/order", h.reorderImages)
	r.With(h.Authz.RequireShopPermission("product.edit")).Put("/products/{productID}/images/{imageID}", h.updateImage)
	r.With(h.Authz.RequireShopPermission("product.edit")).Delete("/products/{productID}/images/{imageID}", h.deleteImage)
}

// MountAdmin: routes under the authenticated /admin group.
func (h *MediaHandler) MountAdmin(r chi.Router) {
	r.Get("/media/config", h.config)
}

func (h *MediaHandler) writeError(w http.ResponseWriter, err error) {
	var ve *media.ValidationError
	var ce *media.ConflictError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, nil)
	case errors.As(err, &ce):
		httpx.Conflict(w, ce.Message)
	case errors.Is(err, media.ErrNotFound):
		httpx.NotFound(w)
	case errors.Is(err, media.ErrNotConfigured):
		httpx.MediaNotConfigured(w)
	default:
		h.Log.Error("media operation failed", "err", err)
		httpx.Internal(w)
	}
}

func (h *MediaHandler) config(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{
		"enabled":                h.Service.Enabled(),
		"public_base_url":        "",
		"max_bytes":              h.Service.Cfg.MaxBytes,
		"accepted_types":         media.AcceptedTypes,
		"max_images_per_product": media.MaxImagesPerProduct,
	}
	if h.Service.Enabled() {
		out["public_base_url"] = h.Service.Cfg.PublicBaseURL
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *MediaHandler) createUpload(w http.ResponseWriter, r *http.Request) {
	if !h.Service.Enabled() {
		httpx.MediaNotConfigured(w)
		return
	}
	shopID, _ := shopIDParam(r)
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	var in media.UploadInput
	if !decodeJSON(w, r, &in) {
		return
	}
	// purpose → permission (design D6), decided here because it is in the body.
	node := ""
	switch in.Purpose {
	case media.PurposeProductImage:
		node = "product.edit"
	case media.PurposeContentImage:
		node = "page.edit"
	default:
		httpx.Unprocessable(w, "purpose must be \"product_image\" or \"content_image\"", nil)
		return
	}
	snap, err := h.Authz.Engine.Snapshot(r.Context(), id.UserID, shopID)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if !snap.Allows(node) {
		httpx.Forbidden(w)
		return
	}
	res, err := h.Service.CreateUpload(r.Context(), shopID, id.UserID, in)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"upload_id":  res.UploadID.String(),
		"upload_url": res.UploadURL,
		"headers":    res.Headers,
		"expires_at": res.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

type attachRequest struct {
	UploadID string `json:"upload_id"`
	Alt      string `json:"alt"`
}

func (h *MediaHandler) parseUploadID(w http.ResponseWriter, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		httpx.Unprocessable(w, "upload_id must be a UUID returned by POST .../media/uploads", nil)
		return uuid.Nil, false
	}
	return id, true
}

func (h *MediaHandler) createContentImage(w http.ResponseWriter, r *http.Request) {
	if !h.Service.Enabled() {
		httpx.MediaNotConfigured(w)
		return
	}
	shopID, _ := shopIDParam(r)
	var in attachRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	uid, ok := h.parseUploadID(w, in.UploadID)
	if !ok {
		return
	}
	res, err := h.Service.CreateContentImage(r.Context(), shopID, uid)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"urls": res.Urls, "width": res.Width, "height": res.Height,
	})
}

func (h *MediaHandler) listImages(w http.ResponseWriter, r *http.Request) {
	if !h.Service.Enabled() {
		httpx.MediaNotConfigured(w)
		return
	}
	shopID, _ := shopIDParam(r)
	pid, ok := intParam(r, "productID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	images, err := h.Service.ListImages(r.Context(), shopID, pid)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"images": h.Service.ImagesJSON(images)})
}

func (h *MediaHandler) attachImage(w http.ResponseWriter, r *http.Request) {
	if !h.Service.Enabled() {
		httpx.MediaNotConfigured(w)
		return
	}
	shopID, _ := shopIDParam(r)
	pid, ok := intParam(r, "productID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in attachRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	uid, ok := h.parseUploadID(w, in.UploadID)
	if !ok {
		return
	}
	im, err := h.Service.AttachProductImage(r.Context(), shopID, pid, uid, in.Alt)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, h.Service.ImageJSON(im))
}

func (h *MediaHandler) updateImage(w http.ResponseWriter, r *http.Request) {
	if !h.Service.Enabled() {
		httpx.MediaNotConfigured(w)
		return
	}
	shopID, _ := shopIDParam(r)
	pid, ok1 := intParam(r, "productID")
	iid, ok2 := intParam(r, "imageID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	var in struct {
		Alt string `json:"alt"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	im, err := h.Service.UpdateAlt(r.Context(), shopID, pid, iid, in.Alt)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.Service.ImageJSON(im))
}

func (h *MediaHandler) reorderImages(w http.ResponseWriter, r *http.Request) {
	if !h.Service.Enabled() {
		httpx.MediaNotConfigured(w)
		return
	}
	shopID, _ := shopIDParam(r)
	pid, ok := intParam(r, "productID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in struct {
		ImageIDs []int `json:"image_ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	images, err := h.Service.Reorder(r.Context(), shopID, pid, in.ImageIDs)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"images": h.Service.ImagesJSON(images)})
}

func (h *MediaHandler) deleteImage(w http.ResponseWriter, r *http.Request) {
	if !h.Service.Enabled() {
		httpx.MediaNotConfigured(w)
		return
	}
	shopID, _ := shopIDParam(r)
	pid, ok1 := intParam(r, "productID")
	iid, ok2 := intParam(r, "imageID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.DeleteImage(r.Context(), shopID, pid, iid); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
