package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/member"
)

// MembersHandler is the merchant back-office customer roster (change
// member-directory). Read-only, RBAC-gated on member.view, and — unlike every
// other handler in this package — its main design content is which fields it
// does NOT emit.
type MembersHandler struct {
	Client  *ent.Client
	Service *member.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountShopAdmin: back-office routes under /admin/shops/{shopID}, gated by
// member.view (mirrors PointsHandler.MountShopAdmin).
//
// Both routes sit on the same chi.Router as the existing
// /members/{memberID}/points* routes — flat patterns, not a sub-router, so the
// contract check's known chi.Walk blind spot (a template carrying both a
// sub-router and method handlers) does not apply here.
func (h *MembersHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("member.view")).Get("/members", h.list)
	r.With(h.Authz.RequireShopPermission("member.view")).Get("/members/{memberID}", h.get)
}

func writeMemberError(w http.ResponseWriter, log *slog.Logger, err error) {
	switch {
	case errors.Is(err, member.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("member operation failed", "err", err)
		httpx.Internal(w)
	}
}

// ── JSON serialization ────────────────────────────────────────────────

// memberJSON is the whole PII disclosure decision of change member-directory,
// expressed as a field list. Every inclusion and every omission is deliberate
// (design D3/D5/D6/D7) — read this before adding a key.
//
// email: INCLUDED. It is the primary identifier and the merchant cannot use the
// roster without it. What this endpoint widens is GRANULARITY (one-at-a-time →
// as a set), not visibility: an order.view holder already reads every order's
// recipient name, phone and full address, page by page. Deliberately NOT masked
// in any form — the detail endpoint has to return the real value anyway (the
// merchant contacts customers with it), so masking would be a screen-only
// gesture: free to defeat with a script, pure friction for a merchant looking
// for "the one starting with wang", and worst of all it would make the platform
// believe it had protected something.
//
// registered: INCLUDED, and it is a boolean projection of `password_hash IS NOT
// NULL` — never the hash, never a fragment of it. It answers "did this person
// actually sign up / can they log in at all", which the merchant needs in order
// to speak correctly to them: a passwordless guest identity CANNOT log in
// (Login's m.PasswordHash == nil branch returns 401), so telling them to "log in
// and check your order" is wrong advice, and nothing else on this payload
// reveals that. Named `registered` rather than `has_password` on purpose: the
// name should state the customer-side fact, not the storage detail — the same
// judgement as AwardBasisAmount over DiscountedSubtotalAmount
// (tier-discount-application design D7).
//
// phone: OMITTED, and this one is evidence rather than preference. No
// production code path writes members.phone — registration takes email only,
// and guest.Service deliberately does not write it (guest-checkout design D2:
// a second uniqueness axis that guest checkout could never explain to a
// customer). The column is therefore always NULL today, and shipping a
// permanently-empty key into v1 repeats exactly the mistake
// tier-discount-application design D1 diagnosed — handing consumers a field
// with nothing behind it. The customer's phone lives in
// orders.shipping_address, which is where it means something ("the delivery
// number for THIS order"), and the order list's `q` already searches it. Adding
// `phone` here is a one-line additive change the day a path writes it.
//
// password_hash: NEVER. Two layers already hold — ent marks the field
// Sensitive() so the generated struct carries `json:"-"`, and every serializer
// in this package is a hand-written map — but both layers can be undone by one
// future line (someone passing `m` straight to WriteJSON, or dropping
// .Sensitive()), and neither would fail a test that only checks the response is
// valid JSON. So the proof lives in members_integration_test.go, asserting on
// the raw response BYTES, and e2e asserts it again against the real server.
//
// order counts / lifetime spend: OMITTED (design D5). Their definition is
// contested — cancelled orders? unpaid? shipping fee? before or after the tier
// discount? — and the screen would show one authoritative-looking number that
// nothing would ever flag as wrong. The detail view shows the customer's actual
// order rows instead, each one clickable and reconcilable.
func memberJSON(m *ent.Member) map[string]any {
	out := map[string]any{
		"id": m.ID, "shop_id": m.ShopID,
		"email": m.Email,
		// password_hash is never emitted; this boolean is all that is derived
		// from it.
		"registered": m.PasswordHash != nil,
		"status":     m.Status,
		// points is a POINT COUNT, not money. It has no "_amount" suffix on
		// purpose: contract check ② would (correctly) demand a MinorUnitAmount
		// $ref from anything named that way, and the admin UI has a matching
		// rule — formatPoints, never formatMoney (admin-commerce-operations
		// design D10).
		"points": m.Points, "level_id": m.LevelID, "level": nil,
		"created_at": m.CreatedAt, "updated_at": m.UpdatedAt,
	}
	if m.Edges.MemberTier != nil {
		out["level"] = memberTierJSON(m.Edges.MemberTier)
	}
	return out
}

func memberPageJSON(mp *member.Page) map[string]any {
	out := make([]map[string]any, 0, len(mp.Members))
	for _, m := range mp.Members {
		out = append(out, memberJSON(m))
	}
	return map[string]any{
		"members": out, "page": mp.Page, "page_size": mp.PageSize, "total": mp.Total,
	}
}

// ── handlers ──────────────────────────────────────────────────────────

func (h *MembersHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	params := member.ListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
		Q:        r.URL.Query().Get("q"),
	}
	mp, err := h.Service.ListMembers(r.Context(), shopID, params)
	if err != nil {
		h.Log.Error("list members", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, memberPageJSON(mp))
}

func (h *MembersHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	memberID, ok := intParam(r, "memberID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	// Two kinds of cross-shop, two different answers (design D8): a caller with
	// no access to this shop was already stopped with 403 by
	// RequireShopPermission's CanAccessShop before reaching here. What can still
	// happen is a caller WITH access naming another shop's member id — the
	// service's explicit shop_id predicate misses, and that is a 404.
	m, err := h.Service.GetMember(r.Context(), shopID, memberID)
	if err != nil {
		writeMemberError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, memberJSON(m))
}
