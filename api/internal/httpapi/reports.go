package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/report"
)

// ReportsHandler is the merchant operating report (change merchant-reports).
// Read-only, RBAC-gated on the single new node report.view.
type ReportsHandler struct {
	Client  *ent.Client
	Service *report.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountShopAdmin registers the report routes under /admin/shops/{shopID}.
func (h *ReportsHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("report.view")).Get("/reports/sales", h.sales)
	r.With(h.Authz.RequireShopPermission("report.view")).Get("/reports/products", h.products)
}

func writeReportError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *report.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.Is(err, report.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("report query failed", "err", err)
		httpx.Internal(w)
	}
}

// requiredDateQuery parses a mandatory YYYY-MM-DD query parameter.
//
// Unlike optionalDateQuery (the order list's tolerant-input rule), a missing or
// malformed value here is a 422 rather than "filter absent": a report has no
// meaningful default period, and silently reporting on some server-chosen range
// would produce numbers the caller cannot reproduce. Returns the zero time on
// failure; the service layer turns that into the 422 with a JSON pointer.
func requiredDateQuery(r *http.Request, name string) time.Time {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}
	}
	d, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}
	}
	return d
}

// ── JSON serialization ────────────────────────────────────────────────

// amountsJSON is the criteria disclosure and the figures in one object.
//
// Naming discipline (design D15): every monetary key ends in `_amount` or
// `_fee` so contract check ② demands a MinorUnitAmount $ref from it, and every
// COUNT deliberately does NOT — order_count named "order_amount" would be a
// count formatted as money on screen, which is the same class of silent error
// as points passing through formatMoney (admin-commerce-operations design D10).
//
// The four counts are a PARTITION of every order created in the period
// (design D4): counted + returned + cancelled + unpaid == the period's total.
// It is what lets a merchant verify the report against the order list instead
// of having to trust it.
func amountsJSON(a report.Amounts) map[string]any {
	return map[string]any{
		"order_count":     a.OrderCount,
		"subtotal_amount": a.SubtotalAmount,
		"discount_amount": a.DiscountAmount,
		// net_amount = subtotal − discount; total_amount = net + shipping_fee.
		// Emitted as five composable numbers rather than one "revenue" figure
		// (design D3 item 5): total_amount's meaning changed in order-extras
		// and its composition changed again in tier-discount-application, and
		// a single number has nowhere to put the answer to "why is this less
		// than I calculated".
		"net_amount":            a.NetAmount,
		"shipping_fee":          a.ShippingFee,
		"total_amount":          a.TotalAmount,
		"returned_order_count":  a.ReturnedOrderCount,
		"returned_total_amount": a.ReturnedTotalAmount,
		"cancelled_order_count": a.CancelledOrderCount,
		"unpaid_order_count":    a.UnpaidOrderCount,
	}
}

func salesReportJSON(rep *report.SalesReport) map[string]any {
	currencies := make([]map[string]any, 0, len(rep.Currencies))
	for _, c := range rep.Currencies {
		buckets := make([]map[string]any, 0, len(c.Buckets))
		for _, b := range c.Buckets {
			bucket := amountsJSON(b.Amounts)
			// A bare calendar day in the shop's own zone — never a timestamp.
			// Emitting an instant here would invite clients to re-apply an
			// offset to a value that has already had one applied, which is the
			// exact shift this report exists to get right (same reasoning as
			// order-extras design D3 for requested_delivery_date).
			bucket["period"] = b.Period
			buckets = append(buckets, bucket)
		}
		currencies = append(currencies, map[string]any{
			"currency": c.Currency,
			"summary":  amountsJSON(c.Summary),
			"buckets":  buckets,
		})
	}
	return map[string]any{
		"shop_id": rep.ShopID, "from": rep.From, "to": rep.To, "group_by": rep.GroupBy,
		"timezone": effectiveTimezoneJSON(rep.Timezone),
		// Currency is the OUTERMOST dimension so that a cross-currency total
		// has nowhere to live (design D13). A single total plus a "mixed
		// currencies" flag would rely on every consumer remembering the flag.
		"currencies": currencies,
	}
}

func productSalesReportJSON(rep *report.ProductSalesReport) map[string]any {
	currencies := make([]map[string]any, 0, len(rep.Currencies))
	for _, c := range rep.Currencies {
		items := make([]map[string]any, 0, len(c.Items))
		for _, it := range c.Items {
			items = append(items, map[string]any{
				// Snapshot values only — no product_id, no sku_id (design D8).
				// order_items' schema states those columns may point at deleted
				// rows and must never be depended on to resolve; emitting them
				// would only let a client build a link that 404s.
				"product_title": it.ProductTitle,
				"sku_code":      it.SKUCode,
				// A COUNT of units, not money: no "_amount" suffix, and the
				// admin must not run it through formatMoney.
				"quantity": it.Quantity,
				// Pre-discount, shipping excluded (design D8).
				"gross_amount": it.GrossAmount,
			})
		}
		currencies = append(currencies, map[string]any{"currency": c.Currency, "items": items})
	}
	return map[string]any{
		"shop_id": rep.ShopID, "from": rep.From, "to": rep.To, "limit": rep.Limit,
		"timezone":   effectiveTimezoneJSON(rep.Timezone),
		"currencies": currencies,
	}
}

// ── handlers ──────────────────────────────────────────────────────────

func (h *ReportsHandler) sales(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = report.GroupByDay
	}
	rep, err := h.Service.Sales(r.Context(), shopID, report.SalesParams{
		From:    requiredDateQuery(r, "from"),
		To:      requiredDateQuery(r, "to"),
		GroupBy: groupBy,
	})
	if err != nil {
		writeReportError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, salesReportJSON(rep))
}

func (h *ReportsHandler) products(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	rep, err := h.Service.Products(r.Context(), shopID, report.ProductParams{
		From:  requiredDateQuery(r, "from"),
		To:    requiredDateQuery(r, "to"),
		Limit: paginationParam(r, "limit"),
	})
	if err != nil {
		writeReportError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, productSalesReportJSON(rep))
}
