package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/shoptz"
)

// DesignAdminHandler is the HUMAN side of the design history (change
// design-history D15): reading the record, and approving, rejecting, asking for
// changes, cancelling and reverting. It lives under /admin/shops/{shopID} and
// is guarded by RBAC (design.*) like every back-office resource. The AI edit
// service has no route here: its credential does not authenticate on /admin.
type DesignAdminHandler struct {
	Service *design.Service
	Client  *ent.Client
	Authz   *AuthzMW
	Limiter *ratelimit.Limiter
	// DefaultTZ resolves the date filters of the list (the shop's own zone when
	// set, as the reports do).
	DefaultTZ string
	Log       *slog.Logger
}

const (
	designApproveLimit  = 20
	designLimitWindow   = time.Minute
	designApprovePrefix = "design_approve"
)

// MountShopAdmin mounts the routes under /admin/shops/{shopID}.
func (h *DesignAdminHandler) MountShopAdmin(r chi.Router) {
	view := h.Authz.RequireShopPermission(design.PermView)
	request := h.Authz.RequireShopPermission(design.PermRequest)
	approve := h.Authz.RequireShopPermission(design.PermApprove)
	revert := h.Authz.RequireShopPermission(design.PermRevert)
	r.Route("/design", func(dr chi.Router) {
		dr.Use(designNoSniff)
		dr.With(view).Get("/requests", h.list)
		dr.With(view).Get("/requests/{requestID}", h.get)
		dr.With(view).Get("/requests/{requestID}/events", h.events)
		dr.With(view).Get("/requests/{requestID}/screenshots", h.screenshots)
		dr.With(approve).Post("/requests/{requestID}/approve", h.approve)
		dr.With(approve).Post("/requests/{requestID}/reject", h.reject)
		dr.With(approve).Post("/requests/{requestID}/request-changes", h.requestChanges)
		dr.With(request).Post("/requests/{requestID}/cancel", h.cancel)
		dr.With(revert).Post("/requests/{requestID}/revert", h.revert)
		dr.With(view).Get("/state", h.state)
		dr.With(view).Get("/untracked-changes", h.untracked)
		dr.With(approve).Post("/untracked-changes/{sha}/acknowledge", h.acknowledge)
	})
}

func (h *DesignAdminHandler) limit(w http.ResponseWriter, r *http.Request, shopID int) bool {
	if h.Limiter == nil {
		return true
	}
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return false
	}
	key := fmt.Sprintf("%s:%d:%d", designApprovePrefix, shopID, id.UserID)
	if res := h.Limiter.Allow(r.Context(), key, designApproveLimit, designLimitWindow); !res.Allowed {
		httpx.TooManyRequests(w, res.RetryAfter)
		return false
	}
	return true
}

// dayBound parses a YYYY-MM-DD in the shop's time zone (or RFC 3339).
func (h *DesignAdminHandler) dayBound(r *http.Request, shopID int, v string, endOfDay bool) (*time.Time, bool) {
	if v == "" {
		return nil, true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, true
	}
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, false
	}
	loc := time.UTC
	if sh, err := h.Client.Shop.Query().Where(shop.IDEQ(shopID)).Only(r.Context()); err == nil {
		if _, l, err := shoptz.Resolve(sh.Timezone, h.DefaultTZ); err == nil {
			loc = l
		}
	}
	t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	if endOfDay {
		t = t.AddDate(0, 0, 1) // the filter is half open: [from, to + 1 day)
	}
	return &t, true
}

func (h *DesignAdminHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	q := r.URL.Query()
	f := design.ListFilter{Site: q.Get("site"), Status: q.Get("status"), Kind: q.Get("kind")}
	f.Page, f.PageSize = queryPage(r)
	if f.Site != "" && !design.IsSite(f.Site) {
		httpx.Unprocessable(w, "site must be storefront or admin", nil)
		return
	}
	if f.Kind != "" && f.Kind != design.KindChange && f.Kind != design.KindRevert {
		httpx.Unprocessable(w, "kind must be change or revert", nil)
		return
	}
	if f.Status != "" {
		ok := false
		for _, s := range design.AllStatuses {
			ok = ok || s == f.Status
		}
		if !ok {
			httpx.Unprocessable(w, "unknown status", nil)
			return
		}
	}
	var ok bool
	if f.From, ok = h.dayBound(r, shopID, q.Get("from"), false); !ok {
		httpx.Unprocessable(w, "from must be YYYY-MM-DD", nil)
		return
	}
	if f.To, ok = h.dayBound(r, shopID, q.Get("to"), true); !ok {
		httpx.Unprocessable(w, "to must be YYYY-MM-DD", nil)
		return
	}
	rows, total, err := h.Service.List(r.Context(), shopID, f)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, designSummaryJSON(row))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"requests": out, "page": f.Page, "page_size": f.PageSize, "total": total})
}

func etagOf(rev int) string { return etagRev(int64(rev)) }

func (h *DesignAdminHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	row, err := h.Service.Get(r.Context(), shopID, id)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	etag := etagOf(row.Rev)
	w.Header().Set("ETag", etag)
	if inm := strings.TrimSpace(r.Header.Get("If-None-Match")); inm != "" && (inm == etag || inm == "W/"+etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, designDetailJSON(row))
}

func (h *DesignAdminHandler) events(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	page, size := queryPage(r)
	rows, total, err := h.Service.Events(r.Context(), shopID, id, page, size)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		out = append(out, designEventJSON(e))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"events": out, "page": page, "page_size": size, "total": total})
}

// screenshots lists the request's screenshots with short-lived signed read
// URLs (design D5). The permission and the request's existence are checked
// first, so a caller learns nothing about the deployment before being
// authorised; the URLs are never logged and the response is never cached.
func (h *DesignAdminHandler) screenshots(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	views, err := h.Service.ListScreenshots(r.Context(), shopID, id, q.Get("phase"), q.Get("all") == "true")
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		m := screenshotJSON(v.Row)
		if v.URL != "" {
			m["url"], m["url_expires_at"] = v.URL, v.URLExpiresAt.UTC()
		} else {
			m["url"], m["url_expires_at"] = nil, nil
		}
		out = append(out, m)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"screenshots": out})
}

type approveBody struct {
	HeadSHA string `json:"head_sha"`
	Note    string `json:"note"`
}

func (h *DesignAdminHandler) approve(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	if !h.limit(w, r, shopID) {
		return
	}
	ifm := r.Header.Get("If-Match")
	if ifm == "" {
		httpx.WriteError(w, http.StatusPreconditionRequired, "precondition_required", `send If-Match: "<rev>" with the revision you reviewed`, nil)
		return
	}
	rev, ok := parseRev(ifm)
	if !ok {
		httpx.BadRequest(w, `If-Match must be the rev of the request, e.g. "7"`)
		return
	}
	var body approveBody
	if !decodeJSON(w, r, &body) {
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	res, err := h.Service.Approve(r.Context(), shopID, id, user.UserID, body.HeadSHA, int(rev), body.Note)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("ETag", etagOf(res.Request.Rev))
	if res.Replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	httpx.WriteJSON(w, http.StatusOK, designDetailJSON(res.Request))
}

func (h *DesignAdminHandler) reject(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	row, err := h.Service.Reject(r.Context(), shopID, id, user.UserID, body.Reason)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("ETag", etagOf(row.Rev))
	httpx.WriteJSON(w, http.StatusOK, designDetailJSON(row))
}

func (h *DesignAdminHandler) requestChanges(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	row, err := h.Service.RequestChanges(r.Context(), shopID, id, user.UserID, body.Note)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("ETag", etagOf(row.Rev))
	httpx.WriteJSON(w, http.StatusOK, designDetailJSON(row))
}

func (h *DesignAdminHandler) cancel(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	row, err := h.Service.Cancel(r.Context(), shopID, id, user.UserID)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("ETag", etagOf(row.Rev))
	httpx.WriteJSON(w, http.StatusOK, designDetailJSON(row))
}

func (h *DesignAdminHandler) revert(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	row, err := h.Service.Revert(r.Context(), shopID, id, user.UserID, body.Note)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("ETag", etagOf(row.Rev))
	httpx.WriteJSON(w, http.StatusCreated, designDetailJSON(row))
}

func siteStateJSON(v design.SiteView) map[string]any {
	m := map[string]any{"site": v.Site, "untracked_changes": v.Untracked, "current_request_id": nil, "current_tag": "",
		"current_sha": "", "core_version": "", "design_floor_version": "", "last_seen_main_sha": "", "updated_at": nil}
	if st := v.State; st != nil {
		m["current_request_id"] = uuidPtr(st.CurrentRequestID)
		m["current_tag"], m["current_sha"] = st.CurrentTag, st.CurrentSha
		m["core_version"], m["design_floor_version"] = st.CoreVersion, st.DesignFloorVersion
		m["last_seen_main_sha"], m["updated_at"] = st.LastSeenMainSha, st.UpdatedAt.UTC()
	}
	return m
}

func (h *DesignAdminHandler) state(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	views, err := h.Service.State(r.Context(), shopID)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		out = append(out, siteStateJSON(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sites": out})
}

func untrackedJSON(u *ent.DesignUntrackedChange) map[string]any {
	return map[string]any{
		"site": u.Site, "commit_sha": u.CommitSha, "committed_at": u.CommittedAt.UTC(), "subject": u.Subject,
		"files_changed": u.FilesChanged, "additions": u.Additions, "deletions": u.Deletions,
		"detected_at": u.DetectedAt.UTC(), "acknowledged_by_user_id": u.AcknowledgedByUserID, "acknowledged_at": ts(u.AcknowledgedAt),
	}
}

func (h *DesignAdminHandler) untracked(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	page, size := queryPage(r)
	site := r.URL.Query().Get("site")
	if site != "" && !design.IsSite(site) {
		httpx.Unprocessable(w, "site must be storefront or admin", nil)
		return
	}
	rows, total, err := h.Service.ListUntracked(r.Context(), shopID, site, page, size)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, u := range rows {
		out = append(out, untrackedJSON(u))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"changes": out, "page": page, "page_size": size, "total": total})
}

func (h *DesignAdminHandler) acknowledge(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	user, _ := auth.AdminFrom(r.Context())
	rows, err := h.Service.AckUntracked(r.Context(), shopID, chi.URLParam(r, "sha"), user.UserID)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, u := range rows {
		out = append(out, untrackedJSON(u))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"changes": out})
}
