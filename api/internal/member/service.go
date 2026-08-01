// Package member is the merchant back-office read path over the customer
// roster (change member-directory).
//
// It is read-only by construction. `members` rows are written by exactly three
// places and none of them is here: registration (internal/auth via
// httpapi/memberauth.go), guest checkout's find-or-create
// (internal/guest), and points.Service's maintenance of points/level_id.
// Adding a write method here would create a fourth, and the first three are the
// ones the specs describe.
//
// TENANCY (change member-identity-per-shop, design D4 — that change's stated
// principal risk): `members` is tenant-owned data registered in
// internal/tenant.tenantOwned, so inside a tenant context every query is
// auto-scoped. Back-office paths run WITHOUT that context. Therefore every
// method below takes shopID as its FIRST parameter and puts it into the
// predicate itself, exactly like points.Service.GetMemberPointsAdmin.
//
// There is deliberately no GetMember(ctx, id) overload and no "convenience"
// lookup that omits the shop. The moment one exists, someone calls it from a
// back-office handler and the isolation is gone silently — nothing would fail,
// the wrong shop's customer would simply be returned.
package member

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ksdevworks/ecommerce/api/internal/ent"
	entmember "ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
)

// ErrNotFound is returned when the member does not exist WITHIN THE GIVEN SHOP.
// A member id belonging to another shop lands here and surfaces as 404, not
// 403: the row is not in this shop, and "does not exist" leaks less than "you
// may not touch that" (which would confirm the id exists somewhere).
var ErrNotFound = errors.New("member not found")

// Service reads the customer roster for a shop.
type Service struct {
	Client *ent.Client
}

// ListParams are pagination + search inputs for the roster.
type ListParams struct {
	Page     int
	PageSize int
	// Q matches the member's email (case-insensitive substring) and, when it
	// parses as a positive integer, additionally the member id — the same shape
	// as the order list's `q` (change admin-commerce-operations, design D3),
	// because a merchant reads "會員 #57" off an order detail and expects to be
	// able to paste 57 into the search box.
	//
	// Substring rather than exact match, and the reasoning is in design D3: the
	// enumeration surface is the LIST ENDPOINT, not the search. A paginated
	// roster can be read page by page regardless. Offering the roster but only
	// exact-match search would add friction for the merchant and nothing for
	// anyone harvesting it.
	Q string
}

// Page is a page of members plus pagination metadata (mirrors order.OrderPage).
type Page struct {
	Members  []*ent.Member
	Total    int
	Page     int
	PageSize int
}

// normalizePage mirrors order/points/catalog: page floor of 1, default size 20,
// hard cap 100. The cap is the only throttle on the roster (design D3 records
// this honestly, along with the fact that there is no domain-level audit trail
// for back-office reads).
func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch {
	case pageSize <= 0:
		pageSize = 20
	case pageSize > 100:
		pageSize = 100
	}
	return page, pageSize
}

// emailOrIDMatches builds the `q` predicate: email substring, OR the member id
// when the keyword is a positive integer.
func emailOrIDMatches(keyword string) predicate.Member {
	preds := []predicate.Member{entmember.EmailContainsFold(keyword)}
	if id, err := strconv.Atoi(keyword); err == nil && id > 0 {
		preds = append(preds, entmember.IDEQ(id))
	}
	return entmember.Or(preds...)
}

// ListMembers returns one page of shopID's customers, newest first.
//
// WithMemberTier eager-loads the tier so the roster can show "who are my VIPs"
// without an N+1. It is the one derived thing the roster carries; deliberately
// NOT carried are order counts and lifetime spend (design D5: their definition
// is undecided — cancelled? unpaid? shipping? pre- or post-discount? — and an
// authoritative-looking wrong number is worse than no number).
func (s *Service) ListMembers(ctx context.Context, shopID int, params ListParams) (*Page, error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)

	q := s.Client.Member.Query().Where(entmember.ShopIDEQ(shopID))
	if kw := strings.TrimSpace(params.Q); kw != "" {
		q = q.Where(emailOrIDMatches(kw))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("member: count: %w", err)
	}
	rows, err := q.
		WithMemberTier().
		Order(ent.Desc(entmember.FieldID)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("member: list: %w", err)
	}
	return &Page{Members: rows, Total: total, Page: page, PageSize: pageSize}, nil
}

// GetMember returns one customer of shopID. The shop_id predicate is explicit
// for the reason in the package doc — back-office paths carry no tenant context.
func (s *Service) GetMember(ctx context.Context, shopID, memberID int) (*ent.Member, error) {
	m, err := s.Client.Member.Query().
		Where(entmember.IDEQ(memberID), entmember.ShopIDEQ(shopID)).
		WithMemberTier().
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("member: get: %w", err)
	}
	return m, nil
}
