package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/content"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/ratelimit"
)

// ContentDocumentsHandler is the admin API of content documents (change
// headless-content-store slice 2, design D5): list, read, save a draft under an
// optimistic lock, publish, unpublish, delete, revisions, restore and preview
// tokens, all under /admin/shops/{shopID}/content.
//
// Permissions: content.view reads, content.edit saves drafts and restores
// (the draft only), content.publish publishes, unpublishes and deletes.
type ContentDocumentsHandler struct {
	Service    *content.Service
	Issuer     *auth.TokenIssuer
	Authz      *AuthzMW
	Limiter    *ratelimit.Limiter
	PreviewTTL time.Duration
	Log        *slog.Logger
}

// Rate limits of design D9, per shop and caller.
const (
	contentSaveLimit    = 60
	contentPublishLimit = 20
	contentLimitWindow  = time.Minute
)

// MountShopAdmin mounts the document routes under /admin/shops/{shopID}.
func (h *ContentDocumentsHandler) MountShopAdmin(r chi.Router) {
	view := h.Authz.RequireShopPermission("content.view")
	edit := h.Authz.RequireShopPermission("content.edit")
	pub := h.Authz.RequireShopPermission("content.publish")
	r.With(view).Get("/content/documents", h.list)
	r.With(view).Get("/content/documents/{key}", h.get)
	r.With(edit).Put("/content/documents/{key}", h.save)
	r.With(pub).Delete("/content/documents/{key}", h.delete)
	r.With(pub).Post("/content/documents/{key}/publish", h.publish)
	r.With(pub).Post("/content/documents/{key}/unpublish", h.unpublish)
	r.With(view).Get("/content/documents/{key}/revisions", h.revisions)
	r.With(view).Get("/content/documents/{key}/revisions/{rev}", h.revision)
	r.With(edit).Post("/content/documents/{key}/revisions/{rev}/restore", h.restore)
	r.With(view).Post("/content/documents/{key}/preview-token", h.previewToken)
}

func (h *ContentDocumentsHandler) limit(w http.ResponseWriter, r *http.Request, scope string, limit int) bool {
	shopID, _ := shopIDParam(r)
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return false
	}
	key := fmt.Sprintf("content_%s:%d:%d", scope, shopID, id.UserID)
	if res := h.Limiter.Allow(r.Context(), key, limit, contentLimitWindow); !res.Allowed {
		httpx.TooManyRequests(w, res.RetryAfter)
		return false
	}
	return true
}

func docSummaryJSON(d *ent.ContentDocument) map[string]any {
	return map[string]any{
		"key": d.Key, "type_key": d.TypeKey, "title": d.Title, "status": d.Status,
		"needs_attention": d.NeedsAttention, "declared": d.Declared,
		"draft_rev": d.DraftRev, "published_rev": d.PublishedRev,
		"schema_version": d.SchemaVersion, "published_schema_version": d.PublishedSchemaVersion,
		"updated_at": d.UpdatedAt, "published_at": d.PublishedAt, "updated_by_user_id": d.UpdatedByUserID,
	}
}

func docDetailJSON(d *ent.ContentDocument, det *content.Detail) map[string]any {
	out := docSummaryJSON(d)
	out["draft"] = rawOrNull(d.DraftJSON)
	out["published"] = rawOrNull(d.PublishedJSON)
	out["meta"] = rawOrNull(d.Meta)
	if det != nil {
		out["schema_node"] = rawOrNull(det.SchemaNode)
		out["type_title"] = det.TypeTitle
	}
	return out
}

func rawOrNull(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func etagRev(rev int64) string { return `"` + strconv.FormatInt(rev, 10) + `"` }

// parseRev reads an If-Match value: "12", W/"12" or 12.
func parseRev(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n >= 1
}

func (h *ContentDocumentsHandler) writeError(w http.ResponseWriter, err error) {
	var ve *cms.ValidationError
	var ce *content.ConflictError
	var se *content.StateError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.As(err, &ce):
		httpx.WriteError(w, http.StatusPreconditionFailed, "precondition_failed", ce.Error(),
			map[string]any{"current_rev": ce.CurrentRev})
	case errors.Is(err, content.ErrDocExists):
		httpx.WriteError(w, http.StatusPreconditionFailed, "precondition_failed", "a document with this key already exists", nil)
	case errors.As(err, &se):
		httpx.WriteError(w, http.StatusConflict, "conflict", se.Message, nil)
	case errors.Is(err, content.ErrNotFound):
		httpx.NotFound(w)
	default:
		h.Log.Error("content documents", "err", err)
		httpx.Internal(w)
	}
}

func (h *ContentDocumentsHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	docs, err := h.Service.List(r.Context(), shopID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, docSummaryJSON(d))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"documents": out})
}

func (h *ContentDocumentsHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	det, err := h.Service.Get(r.Context(), shopID, chi.URLParam(r, "key"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.Header().Set("ETag", etagRev(det.Doc.DraftRev))
	httpx.WriteJSON(w, http.StatusOK, docDetailJSON(det.Doc, det))
}

type saveBody struct {
	TypeKey string          `json:"type_key"`
	Title   *string         `json:"title"`
	Content json.RawMessage `json:"content"`
	Meta    json.RawMessage `json:"meta"`
}

// save handles PUT /content/documents/{key}: an update needs If-Match with the
// draft_rev the caller loaded, a create needs If-None-Match: *; neither is 428.
func (h *ContentDocumentsHandler) save(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "save", contentSaveLimit) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	req := content.SaveRequest{}
	switch {
	case r.Header.Get("If-Match") != "":
		rev, ok := parseRev(r.Header.Get("If-Match"))
		if !ok {
			httpx.BadRequest(w, "If-Match must be the draft_rev of the document, e.g. \"3\"")
			return
		}
		req.IfMatch = &rev
	case strings.TrimSpace(r.Header.Get("If-None-Match")) == "*":
		req.IfNoneMatch = true
	default:
		httpx.WriteError(w, http.StatusPreconditionRequired, "precondition_required",
			"send If-Match: \"<draft_rev>\" to update a document or If-None-Match: * to create one", nil)
		return
	}
	var in saveBody
	if !decodeJSON(w, r, &in) {
		return
	}
	req.TypeKey, req.Title, req.Content, req.Meta = in.TypeKey, in.Title, in.Content, in.Meta
	res, err := h.Service.SaveDraft(r.Context(), shopID, chi.URLParam(r, "key"), req, id.UserID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.Header().Set("ETag", etagRev(res.Doc.DraftRev))
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	// The response carries the value actually stored (rich text may have been
	// rewritten by the sanitizer) so an editor can rebuild its form from it.
	httpx.WriteJSON(w, status, docDetailJSON(res.Doc, nil))
}

func (h *ContentDocumentsHandler) publish(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "publish", contentPublishLimit) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	var expected *int64
	if v := r.Header.Get("If-Match"); v != "" {
		rev, ok := parseRev(v)
		if !ok {
			httpx.BadRequest(w, "If-Match must be the draft_rev of the document")
			return
		}
		expected = &rev
	}
	if r.ContentLength != 0 {
		var body struct {
			ExpectedRev *int64 `json:"expected_rev"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if body.ExpectedRev != nil {
			expected = body.ExpectedRev
		}
	}
	d, err := h.Service.Publish(r.Context(), shopID, chi.URLParam(r, "key"), expected, id.UserID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.Header().Set("ETag", etagRev(d.DraftRev))
	httpx.WriteJSON(w, http.StatusOK, docSummaryJSON(d))
}

func (h *ContentDocumentsHandler) unpublish(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "publish", contentPublishLimit) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	d, err := h.Service.Unpublish(r.Context(), shopID, chi.URLParam(r, "key"), id.UserID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, docSummaryJSON(d))
}

func (h *ContentDocumentsHandler) delete(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "publish", contentPublishLimit) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	if err := h.Service.Delete(r.Context(), shopID, chi.URLParam(r, "key"), id.UserID); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ContentDocumentsHandler) revisions(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	revs, err := h.Service.Revisions(r.Context(), shopID, chi.URLParam(r, "key"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(revs))
	for _, rv := range revs {
		out = append(out, map[string]any{
			"rev_no": rv.RevNo, "kind": rv.Kind, "title": rv.Title, "schema_version": rv.SchemaVersion,
			"actor_user_id": rv.ActorUserID, "created_at": rv.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

func (h *ContentDocumentsHandler) revision(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	n, ok := intParam(r, "rev")
	if !ok {
		httpx.NotFound(w)
		return
	}
	rv, err := h.Service.Revision(r.Context(), shopID, chi.URLParam(r, "key"), n)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"rev_no": rv.RevNo, "kind": rv.Kind, "title": rv.Title, "schema_version": rv.SchemaVersion,
		"actor_user_id": rv.ActorUserID, "created_at": rv.CreatedAt,
		"content": rawOrNull(rv.JSON), "published_content": rawOrNull(rv.PublishedJSON), "meta": rawOrNull(rv.Meta),
	})
}

// restore puts a revision back as the DRAFT. It never publishes.
func (h *ContentDocumentsHandler) restore(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "save", contentSaveLimit) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	n, ok := intParam(r, "rev")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var ifMatch *int64
	if v := r.Header.Get("If-Match"); v != "" {
		rev, ok := parseRev(v)
		if !ok {
			httpx.BadRequest(w, "If-Match must be the draft_rev of the document")
			return
		}
		ifMatch = &rev
	}
	res, err := h.Service.Restore(r.Context(), shopID, chi.URLParam(r, "key"), n, ifMatch, id.UserID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.Header().Set("ETag", etagRev(res.Doc.DraftRev))
	out := docDetailJSON(res.Doc, nil)
	out["restored_valid"] = res.Valid
	httpx.WriteJSON(w, http.StatusOK, out)
}

// previewToken issues a short-lived content preview token (design D2): scope
// "document" (default) is bound to the key, scope "shop" previews every draft.
func (h *ContentDocumentsHandler) previewToken(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	key := chi.URLParam(r, "key")
	scope := auth.PreviewScopeDocument
	if r.ContentLength != 0 {
		var body struct {
			Scope string `json:"scope"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		switch body.Scope {
		case "", auth.PreviewScopeDocument:
		case auth.PreviewScopeShop:
			scope = auth.PreviewScopeShop
		default:
			httpx.Unprocessable(w, "scope must be document or shop", []cms.Detail{{Pointer: "/scope", Message: "document or shop"}})
			return
		}
	}
	if _, err := h.Service.Get(r.Context(), shopID, key); err != nil {
		h.writeError(w, err)
		return
	}
	tokenKey := key
	if scope == auth.PreviewScopeShop {
		tokenKey = ""
	}
	tok, err := h.Issuer.IssueContentPreview(id.UserID, shopID, scope, tokenKey, h.PreviewTTL)
	if err != nil {
		h.Log.Error("issue content preview token", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"preview_token": tok, "expires_in": int(h.PreviewTTL.Seconds()), "scope": scope, "key": key,
	})
}
