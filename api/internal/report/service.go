// Package report is the merchant operating report (change merchant-reports).
//
// It is READ-ONLY by construction: it writes no table, publishes no event and
// invalidates no cache. Everything it returns is an aggregation of orders the
// caller could already have read one page at a time.
//
// ── 口徑 (the counting criteria), and why they are here ─────────────────
//
// A report's failure mode is not "it does not work". It is one
// authoritative-looking wrong number that gets believed, used for decisions,
// and never flagged by anything. So every criterion below is written down, and
// — more importantly — travels in the response body so it cannot drift away
// from the numbers the way a document can (design D12).
//
// Counted toward revenue: status = 0 (not cancelled) AND payment_status = 1
// (paid) AND fulfillment_status <> 3 (not returned).
//
//   - Cancelled: excluded. Cancellation restores stock and claws back points —
//     the system already treats it as not having happened.
//   - Unpaid: excluded. An unpaid order may never be paid (guest checkout can
//     leave one behind), and counting it makes the report permanently exceed
//     the bank account.
//   - Returned: excluded from the revenue lines but reported SEPARATELY, never
//     erased. "Three came back this month" is the single thing a merchant most
//     needs to see, and hiding it is the first step toward a report that
//     flatters.
//
// Timeline: orders.created_at, not payment time and not shipment time (design
// D3 reason 4). **This report is therefore not a cash-flow statement**, and the
// UI says so.
//
// ── The reconciliation partition (design D4) ───────────────────────────
//
// Every order created in the period lands in exactly one of four buckets:
// cancelled, unpaid, returned, counted. Their sum equals the period's total
// order count, so the merchant can verify the report against the order list
// instead of having to trust it. The classes are evaluated IN ORDER because
// they overlap in the data (a cancelled order is necessarily unpaid); four
// independent counts would double-count it, and "the four numbers add up to one
// more than the order list says" is an error nobody spots.
//
// ── SQL, and why it is raw ─────────────────────────────────────────────
//
// ent cannot express `AT TIME ZONE`: there is no predicate for it and no
// GroupBy of that shape. So the two aggregates below go through
// ent.Client.QueryContext (the sql/execquery feature, already enabled) with
// native $N placeholders — values are always bound, never concatenated. The
// only interpolated fragment is the bucket expression, chosen from a fixed
// two-element set in this file, never from caller input.
//
// Raw SQL bypasses the tenant interceptor entirely, exactly like the rest of
// the back office (which runs outside tenant context anyway). **Therefore every
// WHERE below carries shop_id explicitly** — here that is the only line of
// defense, not a second one.
package report

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/shoptz"
)

// ErrNotFound marks a missing shop (handler → 404).
var ErrNotFound = errors.New("report: shop not found")

// Detail locates one validation problem (same convention as
// order.Detail/cart.Detail).
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads (design D12 error envelope).
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

func validationErr(pointer, msg string) *ValidationError {
	return &ValidationError{Message: msg, Details: []Detail{{Pointer: pointer, Message: msg}}}
}

// Grouping granularity.
const (
	GroupByDay   = "day"
	GroupByMonth = "month"
)

// Period length caps. They bound the response size rather than the query cost;
// without them one request can ask for ten thousand daily buckets.
//
// Two different caps because the buckets, not the days, are what grows: 366
// daily buckets is a year, 1830 monthly buckets would be 150 years, so the
// monthly cap is expressed in days too and lands at five years.
const (
	maxDaySpanDays   = 366
	maxMonthSpanDays = 1830
	dateLayout       = "2006-01-02"
)

// The counting criteria, as SQL fragments. Written once and referenced by both
// queries, so the ranking can never drift from the revenue report.
const (
	countedOrders   = "o.status = 0 AND o.payment_status = 1 AND o.fulfillment_status <> 3"
	returnedOrders  = "o.status = 0 AND o.payment_status = 1 AND o.fulfillment_status = 3"
	cancelledOrders = "o.status = 1"
	unpaidOrders    = "o.status = 0 AND o.payment_status <> 1"
)

// Service reads aggregates for one shop.
//
// DefaultTimezone is the platform fallback (config.ReportDefaultTimezone).
// It is a plain string, so the wiring audit cannot report it as unset —
// shoptz.Resolve rejects an empty value loudly for exactly that reason.
type Service struct {
	Client          *ent.Client
	DefaultTimezone string
}

// Amounts is one set of figures: the revenue lines, the returned line, and the
// two remaining classes of the reconciliation partition.
//
// Every field is a SUM or a COUNT of int64s. **There is no division anywhere in
// this package** (design D11): a division needs a rounding direction, this
// system has exactly one (truncate), and an average that is one dollar off is
// the kind of wrong that looks right forever. order_count and total_amount are
// both on screen; a merchant who wants a mean can divide them.
type Amounts struct {
	OrderCount     int
	SubtotalAmount int64
	DiscountAmount int64
	// NetAmount = SubtotalAmount − DiscountAmount. Derived by addition, which
	// is exact and order-independent, so summing buckets and summing rows give
	// the same answer.
	NetAmount   int64
	ShippingFee int64
	TotalAmount int64

	ReturnedOrderCount  int
	ReturnedTotalAmount int64

	CancelledOrderCount int
	UnpaidOrderCount    int
}

func (a *Amounts) add(o Amounts) {
	a.OrderCount += o.OrderCount
	a.SubtotalAmount += o.SubtotalAmount
	a.DiscountAmount += o.DiscountAmount
	a.NetAmount += o.NetAmount
	a.ShippingFee += o.ShippingFee
	a.TotalAmount += o.TotalAmount
	a.ReturnedOrderCount += o.ReturnedOrderCount
	a.ReturnedTotalAmount += o.ReturnedTotalAmount
	a.CancelledOrderCount += o.CancelledOrderCount
	a.UnpaidOrderCount += o.UnpaidOrderCount
}

// Bucket is one local day (or month) of figures.
type Bucket struct {
	// Period is a bare calendar day, YYYY-MM-DD, in the shop's effective zone.
	// Month grouping reports the first day of the month rather than "2026-08",
	// so one type serves both and clients never parse two shapes.
	Period string
	Amounts
}

// CurrencySales is one currency's complete, self-contained report.
type CurrencySales struct {
	Currency string
	Summary  Amounts
	Buckets  []Bucket
}

// SalesReport is the whole answer.
//
// Currencies is the OUTERMOST dimension on purpose (design D13): there is
// nowhere in this structure to put a cross-currency total, so the platform can
// never accidentally produce one. The alternative — one total plus a flag
// saying "careful, mixed currencies" — relies on every consumer remembering to
// check the flag.
type SalesReport struct {
	ShopID     int
	From       string
	To         string
	GroupBy    string
	Timezone   shoptz.Effective
	Currencies []CurrencySales
}

// SalesParams are the request inputs. From/To are calendar days in the shop's
// effective zone, both inclusive.
//
// Both are REQUIRED (no zero-value default period): a report whose period the
// caller did not state is a report whose numbers cannot be reproduced.
type SalesParams struct {
	From    time.Time
	To      time.Time
	GroupBy string
}

// resolveShop loads the shop and its effective time zone.
func (s *Service) resolveShop(ctx context.Context, shopID int) (shoptz.Effective, *time.Location, error) {
	sh, err := s.Client.Shop.Query().Where(entshop.IDEQ(shopID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return shoptz.Effective{}, nil, ErrNotFound
		}
		return shoptz.Effective{}, nil, fmt.Errorf("report: load shop: %w", err)
	}
	eff, loc, err := shoptz.Resolve(sh.Timezone, s.DefaultTimezone)
	if err != nil {
		return shoptz.Effective{}, nil, fmt.Errorf("report: resolve shop %d timezone: %w", shopID, err)
	}
	return eff, loc, nil
}

// periodBounds turns two local calendar days into the half-open UTC instant
// range [start, end) that covers them.
//
// **Half-open, not BETWEEN** (design D5): `created_at BETWEEN a AND b` includes
// the instant b exactly, so an order placed at 00:00:00.000000 on the day after
// the range would be counted. That bug appears only for orders placed at
// midnight sharp, which is to say it appears in production and never in a test
// somebody wrote by hand.
//
// The bounds are computed in the SAME location the buckets are cut with, which
// is the property test 3 of design D16 pins: cutting buckets in the shop's zone
// while bounding the range in UTC puts orders in a bucket that lies outside the
// range they were selected by.
func periodBounds(from, to time.Time, loc *time.Location) (time.Time, time.Time) {
	y1, m1, d1 := from.Date()
	y2, m2, d2 := to.Date()
	start := time.Date(y1, m1, d1, 0, 0, 0, 0, loc)
	end := time.Date(y2, m2, d2, 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	return start, end
}

func validateParams(p SalesParams) error {
	if p.From.IsZero() {
		return validationErr("/from", "from is required (YYYY-MM-DD)")
	}
	if p.To.IsZero() {
		return validationErr("/to", "to is required (YYYY-MM-DD)")
	}
	if p.GroupBy != GroupByDay && p.GroupBy != GroupByMonth {
		return validationErr("/group_by", `group_by must be "day" or "month"`)
	}
	// Compare the calendar days themselves rather than the instants: the two
	// values carry no location yet.
	if p.To.Before(p.From) {
		return validationErr("/to", "to must not be earlier than from")
	}
	span := int(p.To.Sub(p.From).Hours()/24) + 1
	limit := maxDaySpanDays
	if p.GroupBy == GroupByMonth {
		limit = maxMonthSpanDays
	}
	if span > limit {
		return validationErr("/to", fmt.Sprintf("period is too long: %d days requested, %d is the maximum for group_by=%s",
			span, limit, p.GroupBy))
	}
	return nil
}

// bucketExpr is the local-day (or local-month) grouping expression.
//
// $1 is the time zone NAME, bound as a parameter — never interpolated. The
// surrounding expression is picked from this fixed set, so nothing from the
// request reaches the SQL text.
func bucketExpr(groupBy string) string {
	if groupBy == GroupByMonth {
		return "date_trunc('month', o.created_at AT TIME ZONE $1)::date"
	}
	return "(o.created_at AT TIME ZONE $1)::date"
}

// Sales returns the period's revenue report, grouped by local day or month.
//
// ONE query. Every figure — the four partition classes and the four money
// columns — comes out of the same GROUP BY via FILTER clauses, so the classes
// cannot disagree about which orders they saw. The summary is then the buckets
// added together in Go, which is int64 addition and nothing else.
//
// Every amount is cast to bigint in SQL. sum(bigint) is numeric in Postgres,
// and an explicit cast means an impossible total raises an error rather than
// arriving quietly truncated.
func (s *Service) Sales(ctx context.Context, shopID int, params SalesParams) (*SalesReport, error) {
	if err := validateParams(params); err != nil {
		return nil, err
	}
	eff, loc, err := s.resolveShop(ctx, shopID)
	if err != nil {
		return nil, err
	}
	start, end := periodBounds(params.From, params.To, loc)

	query := fmt.Sprintf(`
SELECT
  %[1]s AS period,
  o.currency,
  count(*) FILTER (WHERE %[2]s)                                      AS order_count,
  coalesce(sum(o.subtotal_amount) FILTER (WHERE %[2]s), 0)::bigint   AS subtotal_amount,
  coalesce(sum(o.discount_amount) FILTER (WHERE %[2]s), 0)::bigint   AS discount_amount,
  coalesce(sum(o.shipping_fee)    FILTER (WHERE %[2]s), 0)::bigint   AS shipping_fee,
  coalesce(sum(o.total_amount)    FILTER (WHERE %[2]s), 0)::bigint   AS total_amount,
  count(*) FILTER (WHERE %[3]s)                                      AS returned_order_count,
  coalesce(sum(o.total_amount)    FILTER (WHERE %[3]s), 0)::bigint   AS returned_total_amount,
  count(*) FILTER (WHERE %[4]s)                                      AS cancelled_order_count,
  count(*) FILTER (WHERE %[5]s)                                      AS unpaid_order_count
FROM orders o
WHERE o.shop_id = $2 AND o.created_at >= $3 AND o.created_at < $4
GROUP BY 1, 2
ORDER BY 1, 2`,
		bucketExpr(params.GroupBy), countedOrders, returnedOrders, cancelledOrders, unpaidOrders)

	rows, err := s.Client.QueryContext(ctx, query, eff.Name, shopID, start, end)
	if err != nil {
		return nil, fmt.Errorf("report: sales query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Keyed by currency, in first-seen order — which, because the query is
	// ORDER BY period, currency, is ascending currency order overall.
	byCurrency := map[string]*CurrencySales{}
	var order []string
	for rows.Next() {
		var period time.Time
		var currency string
		var a Amounts
		if err := rows.Scan(
			&period, &currency,
			&a.OrderCount, &a.SubtotalAmount, &a.DiscountAmount, &a.ShippingFee, &a.TotalAmount,
			&a.ReturnedOrderCount, &a.ReturnedTotalAmount,
			&a.CancelledOrderCount, &a.UnpaidOrderCount,
		); err != nil {
			return nil, fmt.Errorf("report: scan sales row: %w", err)
		}
		a.NetAmount = a.SubtotalAmount - a.DiscountAmount

		cs, ok := byCurrency[currency]
		if !ok {
			cs = &CurrencySales{Currency: currency}
			byCurrency[currency] = cs
			order = append(order, currency)
		}
		cs.Buckets = append(cs.Buckets, Bucket{Period: period.Format(dateLayout), Amounts: a})
		cs.Summary.add(a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("report: sales rows: %w", err)
	}

	out := &SalesReport{
		ShopID: shopID,
		From:   params.From.Format(dateLayout),
		To:     params.To.Format(dateLayout),
		// The value actually used, which is params.GroupBy after validation —
		// echoed back so a client never has to assume the server's default.
		GroupBy:    params.GroupBy,
		Timezone:   eff,
		Currencies: make([]CurrencySales, 0, len(order)),
	}
	sortCurrencies(order)
	for _, c := range order {
		out.Currencies = append(out.Currencies, *byCurrency[c])
	}
	return out, nil
}

// ── 商品銷售排行 ──────────────────────────────────────────────────────

// ProductRow is one ranked line.
//
// **No product_id and no sku_id** (design D8). order_items' own schema says
// those columns are informational, may point at deleted rows, and that callers
// must never depend on them resolving — so grouping by them would violate a
// documented contract, and emitting them would only let a client build a link
// that 404s.
type ProductRow struct {
	ProductTitle string
	SKUCode      string
	Quantity     int64
	// GrossAmount is price × quantity, BEFORE any discount, and excluding
	// shipping (design D8). The order-level discount is a percentage of the
	// whole order; apportioning it back to lines needs a rule, and every
	// possible rule produces a per-product figure that reconciles against no
	// document. So this ranks "how much was sold", not "how much this product
	// contributed", and the UI says which.
	GrossAmount int64
}

// CurrencyProducts is one currency's ranking.
type CurrencyProducts struct {
	Currency string
	Items    []ProductRow
}

// ProductSalesReport mirrors SalesReport's currency-outermost shape for the
// same reason.
type ProductSalesReport struct {
	ShopID     int
	From       string
	To         string
	Limit      int
	Timezone   shoptz.Effective
	Currencies []CurrencyProducts
}

// ProductParams are the ranking's inputs.
type ProductParams struct {
	From  time.Time
	To    time.Time
	Limit int
}

const (
	defaultProductLimit = 20
	maxProductLimit     = 100
)

func normalizeLimit(n int) int {
	switch {
	case n <= 0:
		return defaultProductLimit
	case n > maxProductLimit:
		return maxProductLimit
	default:
		return n
	}
}

// Products ranks the period's sales by product snapshot.
//
// It uses the SAME criteria constants as Sales, so the ranking can never
// include an order the revenue report excluded.
//
// **It needs no AT TIME ZONE**: the period is already two instants by the time
// the query runs. The zone still matters — it decided those instants — and is
// reported back for that reason.
//
// The grouping key is (product_title, sku_code, currency): all three are
// NOT NULL checkout snapshots that always exist and always match what is
// written on the order. A renamed product therefore produces two rows, and that
// is honest — those order lines really do carry two different names.
func (s *Service) Products(ctx context.Context, shopID int, params ProductParams) (*ProductSalesReport, error) {
	if err := validateParams(SalesParams{From: params.From, To: params.To, GroupBy: GroupByDay}); err != nil {
		return nil, err
	}
	eff, loc, err := s.resolveShop(ctx, shopID)
	if err != nil {
		return nil, err
	}
	limit := normalizeLimit(params.Limit)
	start, end := periodBounds(params.From, params.To, loc)

	// oi.shop_id AND o.shop_id are both constrained: the join could otherwise
	// be satisfied through either side alone, and defense in depth here costs
	// one predicate.
	//
	// The ORDER BY carries two tiebreakers past the obvious ones so that two
	// lines with identical money and quantity have a fixed relative order —
	// "usually the same" is not an order (commerce-demo-seed design D8).
	query := fmt.Sprintf(`
SELECT
  oi.product_title,
  oi.sku_code,
  oi.currency,
  coalesce(sum(oi.quantity), 0)::bigint                    AS quantity,
  coalesce(sum(oi.price_amount * oi.quantity), 0)::bigint  AS gross_amount
FROM order_items oi
JOIN orders o ON o.id = oi.order_id
WHERE oi.shop_id = $1 AND o.shop_id = $1
  AND o.created_at >= $2 AND o.created_at < $3
  AND %s
GROUP BY 1, 2, 3
ORDER BY gross_amount DESC, quantity DESC, oi.sku_code ASC, oi.product_title ASC`, countedOrders)

	rows, err := s.Client.QueryContext(ctx, query, shopID, start, end)
	if err != nil {
		return nil, fmt.Errorf("report: product query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byCurrency := map[string]*CurrencyProducts{}
	var order []string
	for rows.Next() {
		var row ProductRow
		var currency string
		if err := rows.Scan(&row.ProductTitle, &row.SKUCode, &currency, &row.Quantity, &row.GrossAmount); err != nil {
			return nil, fmt.Errorf("report: scan product row: %w", err)
		}
		cp, ok := byCurrency[currency]
		if !ok {
			cp = &CurrencyProducts{Currency: currency}
			byCurrency[currency] = cp
			order = append(order, currency)
		}
		// The limit applies PER CURRENCY, so a shop selling in two currencies
		// gets a usable ranking in each rather than one list dominated by
		// whichever currency has the larger numbers.
		if len(cp.Items) < limit {
			cp.Items = append(cp.Items, row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("report: product rows: %w", err)
	}

	out := &ProductSalesReport{
		ShopID: shopID,
		From:   params.From.Format(dateLayout),
		To:     params.To.Format(dateLayout),
		Limit:  limit, Timezone: eff,
		Currencies: make([]CurrencyProducts, 0, len(order)),
	}
	sortCurrencies(order)
	for _, c := range order {
		out.Currencies = append(out.Currencies, *byCurrency[c])
	}
	return out, nil
}

// sortCurrencies fixes the order of the outermost dimension. Ascending ISO 4217
// code: arbitrary, but stated and stable, which is the requirement
// (commerce-demo-seed design D8 — a collection endpoint must define its order
// rather than inherit whatever the database returns).
func sortCurrencies(codes []string) { sort.Strings(codes) }

// compile-time assertion that the ent client really exposes the raw query
// escape hatch this package depends on (the sql/execquery feature). Without it
// the failure would be a runtime "Driver.QueryContext is not supported" on the
// first report.
var _ interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
} = (*ent.Client)(nil)
