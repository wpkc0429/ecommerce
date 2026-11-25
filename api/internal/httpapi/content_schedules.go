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

// ContentSchedulesHandler is the admin API of scheduled publishing (change
// headless-content-store slice 3, design D11): schedule, reschedule and cancel
// a document's publish, and list a shop's upcoming and recent schedules.
//
// content.publish creates, changes and cancels (the same permission as a manual
// publish: `editor` has none); content.view reads.
type ContentSchedulesHandler struct {
	Scheduler *content.Scheduler
	Authz     *AuthzMW
	Limiter   *ratelimit.Limiter
	Log       *slog.Logger
}

// MountShopAdmin mounts the routes under /admin/shops/{shopID}.
func (h *ContentSchedulesHandler) MountShopAdmin(r chi.Router) {
	view := h.Authz.RequireShopPermission("content.view")
	pub := h.Authz.RequireShopPermission("content.publish")
	r.With(view).Get("/content/schedules", h.list)
	r.With(view).Get("/content/documents/{key}/schedule", h.get)
	r.With(pub).Put("/content/documents/{key}/schedule", h.put)
	r.With(pub).Delete("/content/documents/{key}/schedule", h.cancel)
}

func scheduleJSON(s *ent.ContentSchedule) map[string]any {
	var failure any
	if len(s.Failure) > 0 {
		failure = json.RawMessage(s.Failure)
	}
	return map[string]any{
		"id": s.ID, "key": s.DocKey, "draft_rev": s.DraftRev,
		"publish_at": s.PublishAt.UTC(), "status": s.Status, "result": s.Result,
		"failure": failure, "attempts": s.Attempts, "late_seconds": s.LateSeconds,
		"created_by_user_id": s.CreatedByUserID, "created_at": s.CreatedAt,
		"finished_at": s.FinishedAt,
	}
}

func (h *ContentSchedulesHandler) writeError(w http.ResponseWriter, err error) {
	var ve *cms.ValidationError
	var ce *content.ConflictError
	var se *content.StateError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.As(err, &ce):
		httpx.WriteError(w, http.StatusPreconditionFailed, "precondition_failed", ce.Error(),
			map[string]any{"current_rev": ce.CurrentRev})
	case errors.As(err, &se):
		httpx.WriteError(w, http.StatusConflict, "conflict", se.Message, nil)
	case errors.Is(err, content.ErrNotFound):
		httpx.NotFound(w)
	default:
		h.Log.Error("content schedules", "err", err)
		httpx.Internal(w)
	}
}

func (h *ContentSchedulesHandler) limit(w http.ResponseWriter, r *http.Request) bool {
	shopID, _ := shopIDParam(r)
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return false
	}
	key := fmt.Sprintf("content_schedule:%d:%d", shopID, id.UserID)
	if res := h.Limiter.Allow(r.Context(), key, contentPublishLimit, contentLimitWindow); !res.Allowed {
		httpx.TooManyRequests(w, res.RetryAfter)
		return false
	}
	return true
}

func (h *ContentSchedulesHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	scope := content.ScopeUpcoming
	switch r.URL.Query().Get("scope") {
	case "", content.ScopeUpcoming:
	case content.ScopeRecent:
		scope = content.ScopeRecent
	default:
		httpx.Unprocessable(w, "scope must be upcoming or recent", []cms.Detail{{Pointer: "/scope", Message: "upcoming or recent"}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.Scheduler.List(r.Context(), shopID, scope, limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, scheduleJSON(s))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (h *ContentSchedulesHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	s, err := h.Scheduler.Get(r.Context(), shopID, chi.URLParam(r, "key"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, scheduleJSON(s))
}

type scheduleBody struct {
	PublishAt *string `json:"publish_at"`
}

// put creates or changes the schedule. The draft_rev the caller reviewed is
// required in If-Match (428 otherwise): the schedule publishes exactly that
// draft, and only if it is still the current one when the time comes.
func (h *ContentSchedulesHandler) put(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	v := r.Header.Get("If-Match")
	if v == "" {
		httpx.WriteError(w, http.StatusPreconditionRequired, "precondition_required",
			"send If-Match: \"<draft_rev>\" - the draft revision you reviewed and want published", nil)
		return
	}
	rev, ok := parseRev(v)
	if !ok {
		httpx.BadRequest(w, "If-Match must be the draft_rev of the document, e.g. \"3\"")
		return
	}
	var in scheduleBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.PublishAt == nil || strings.TrimSpace(*in.PublishAt) == "" {
		httpx.Unprocessable(w, "publish_at is required", []cms.Detail{{Pointer: "/publish_at", Message: "required (RFC 3339 with a UTC offset)"}})
		return
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(*in.PublishAt))
	if err != nil {
		httpx.Unprocessable(w, "publish_at must be an RFC 3339 time with a UTC offset, e.g. 2026-12-01T09:00:00+08:00",
			[]cms.Detail{{Pointer: "/publish_at", Message: "RFC 3339 with an explicit offset or Z"}})
		return
	}
	s, created, err := h.Scheduler.Schedule(r.Context(), shopID, chi.URLParam(r, "key"), at, rev, id.UserID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, scheduleJSON(s))
}

func (h *ContentSchedulesHandler) cancel(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	if err := h.Scheduler.Cancel(r.Context(), shopID, chi.URLParam(r, "key"), id.UserID); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
