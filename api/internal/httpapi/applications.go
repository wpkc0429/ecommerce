package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/application"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/rbac"
)

// ApplicationsHandler is the operator side of merchant applications: list,
// view, approve, reject, manual verification and outbox hand-over (change
// platform-website, design D5/D9/D10). Platform level only: it sits behind the
// platform admin domain and Cloudflare Access, and every route needs
// shop_application.view or .review, which only super_admin holds.
type ApplicationsHandler struct {
	Service *application.Service
	Engine  *rbac.Engine
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountPlatform registers /admin/shop-applications*.
func (h *ApplicationsHandler) MountPlatform(r chi.Router) {
	view := h.Authz.RequirePlatformPermission("shop_application.view")
	review := h.Authz.RequirePlatformPermission("shop_application.review")
	r.With(view).Get("/shop-applications", h.list)
	r.With(view).Get("/shop-applications/{id}", h.get)
	r.With(review).Post("/shop-applications/{id}/approve", h.approve)
	r.With(review).Post("/shop-applications/{id}/reject", h.reject)
	r.With(review).Post("/shop-applications/{id}/verify-manually", h.verifyManually)
	r.With(review).Post("/shop-applications/{id}/messages/{mid}/mark-sent", h.markSent)
}

func applicationJSON(a *ent.ShopApplication, full bool) map[string]any {
	out := map[string]any{
		"id":                  a.ID,
		"status":              a.Status,
		"email":               a.Email,
		"email_verified":      a.EmailVerifiedAt != nil,
		"email_verified_at":   a.EmailVerifiedAt,
		"contact_name":        a.ContactName,
		"shop_name":           a.ShopName,
		"risk_flags":          nonNilFlags(a.RiskFlags),
		"reviewed_by":         a.ReviewedBy,
		"reviewed_at":         a.ReviewedAt,
		"provisioned_shop_id": a.ProvisionedShopID,
		"purged":              a.PurgedAt != nil,
		"created_at":          a.CreatedAt,
	}
	if full {
		out["phone"] = a.Phone
		out["business_summary"] = a.BusinessSummary
		out["consent_version"] = a.ConsentVersion
		out["consented_at"] = a.ConsentedAt
		out["decision_reason"] = a.DecisionReason
		out["internal_note"] = a.InternalNote
	}
	return out
}

func nonNilFlags(m map[string]bool) map[string]bool {
	if m == nil {
		return map[string]bool{}
	}
	return m
}

func (h *ApplicationsHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("page_size"))
	res, err := h.Service.List(r.Context(), application.ListFilter{
		Status: q.Get("status"), Q: q.Get("q"), Page: page, Size: size,
	})
	if err != nil {
		h.Log.Error("applications: list", "err", err)
		httpx.Internal(w)
		return
	}
	items := make([]map[string]any, 0, len(res.Items))
	for _, a := range res.Items {
		items = append(items, applicationJSON(a, false))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": res.Total,
		"counts": res.Counts, "pending_messages": res.PendingMessages,
	})
}

func (h *ApplicationsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "id")
	if !ok {
		httpx.NotFound(w)
		return
	}
	d, err := h.Service.Get(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	app := applicationJSON(d.App, true)
	app["email_has_account"] = d.EmailHasAccount
	events := make([]map[string]any, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, map[string]any{
			"id": e.ID, "event": e.Event, "actor_user_id": e.ActorUserID,
			"detail": e.Detail, "created_at": e.CreatedAt,
		})
	}
	msgs := make([]map[string]any, 0, len(d.Messages))
	for _, m := range d.Messages {
		msgs = append(msgs, map[string]any{
			"id": m.ID, "kind": m.Kind, "recipient_email": m.RecipientEmail, "subject": m.Subject,
			"body_text": m.BodyText, "delivery": m.Delivery, "status": m.Status,
			"error_reason": m.ErrorReason, "created_at": m.CreatedAt, "sent_at": m.SentAt, "marked_by": m.MarkedBy,
		})
	}
	app["events"] = events
	app["messages"] = msgs
	httpx.WriteJSON(w, http.StatusOK, app)
}

func (h *ApplicationsHandler) approve(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	id, ok := intParam(r, "id")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var req struct {
		ShopName               string `json:"shop_name"`
		Status                 *int16 `json:"status"`
		ConfirmExistingAccount bool   `json:"confirm_existing_account"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// Approval opens a shop and an owner account: it needs exactly what
	// POST /admin/shop-provisioning needs, on top of the review permission.
	snap, err := h.Engine.Snapshot(r.Context(), actor.UserID, rbac.PlatformCtx)
	if err != nil {
		h.Log.Error("applications: snapshot", "err", err)
		httpx.Internal(w)
		return
	}
	for _, n := range []string{"shop.create", "account.manage"} {
		if !snap.Allows(n) {
			httpx.Forbidden(w)
			return
		}
	}
	res, err := h.Service.Approve(r.Context(), id, actor.UserID, application.ApproveInput{
		ShopName: req.ShopName, Status: req.Status, ConfirmExistingAccount: req.ConfirmExistingAccount,
	})
	if err != nil {
		h.writeErr(w, err)
		return
	}
	owner := map[string]any{"user_id": res.Provision.OwnerID, "email": res.Provision.OwnerEmail, "created": res.Provision.Created}
	if res.Provision.Delivery != nil {
		for k, v := range linkJSON(account.KindSetup, *res.Provision.Delivery) {
			owner[k] = v
		}
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"application": applicationJSON(res.App, true),
		"shop":        shopJSON(res.Provision.Shop),
		"owner":       owner,
	})
}

func (h *ApplicationsHandler) reject(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	id, idOK := intParam(r, "id")
	if !ok || !idOK {
		httpx.NotFound(w)
		return
	}
	var req struct {
		DecisionReason string `json:"decision_reason"`
		InternalNote   string `json:"internal_note"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.Service.Reject(r.Context(), id, actor.UserID, req.DecisionReason, req.InternalNote); err != nil {
		h.writeErr(w, err)
		return
	}
	d, err := h.Service.Get(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, applicationJSON(d.App, true))
}

func (h *ApplicationsHandler) verifyManually(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	id, idOK := intParam(r, "id")
	if !ok || !idOK {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.VerifyManually(r.Context(), id, actor.UserID); err != nil {
		h.writeErr(w, err)
		return
	}
	d, err := h.Service.Get(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, applicationJSON(d.App, true))
}

func (h *ApplicationsHandler) markSent(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	id, idOK := intParam(r, "id")
	mid, midOK := intParam(r, "mid")
	if !ok || !idOK || !midOK {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.MarkMessageSent(r.Context(), id, mid, actor.UserID); err != nil {
		h.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": mid, "status": "sent"})
}

func (h *ApplicationsHandler) writeErr(w http.ResponseWriter, err error) {
	var ve *application.ValidationError
	var cve *cms.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, []httpx.ValidationDetail{{Pointer: ve.Pointer, Message: ve.Message}})
	case errors.As(err, &cve):
		httpx.Unprocessable(w, cve.Message, cve.Details)
	case errors.Is(err, application.ErrNotFound):
		httpx.NotFound(w)
	case errors.Is(err, application.ErrExistingAccount):
		httpx.WriteError(w, http.StatusConflict, "confirm_existing_account_required", err.Error(), nil)
	case errors.Is(err, application.ErrConflict):
		httpx.Conflict(w, err.Error())
	default:
		writeAccountError(w, h.Log, err)
	}
}
