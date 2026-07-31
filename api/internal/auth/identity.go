package auth

import "context"

// AdminIdentity is the authenticated back-office principal.
type AdminIdentity struct {
	UserID int
	SIDs   []int // shop membership hints from the token (UI only)
}

type adminKey struct{}

// WithAdminIdentity attaches the authenticated admin principal to ctx.
func WithAdminIdentity(ctx context.Context, id AdminIdentity) context.Context {
	return context.WithValue(ctx, adminKey{}, id)
}

// AdminFrom returns the authenticated admin principal, if any.
func AdminFrom(ctx context.Context) (AdminIdentity, bool) {
	id, ok := ctx.Value(adminKey{}).(AdminIdentity)
	return id, ok
}

type memberKey struct{}

// WithMemberID attaches the authenticated member id to ctx.
func WithMemberID(ctx context.Context, memberID int) context.Context {
	return context.WithValue(ctx, memberKey{}, memberID)
}

// MemberFrom returns the authenticated member id, if any.
func MemberFrom(ctx context.Context) (int, bool) {
	id, ok := ctx.Value(memberKey{}).(int)
	return id, ok
}

type orderAccessKey struct{}

// WithOrderAccess attaches the order id named by a verified order access token
// (change guest-checkout, design D4).
//
// Deliberately a separate context key from memberKey: an order access token
// MUST NOT satisfy anything that asks for a member identity. Should a guest
// route ever be mis-mounted under the member group, memberFrom simply misses
// and the handler answers 401 rather than treating an order id as a member id.
func WithOrderAccess(ctx context.Context, orderID int) context.Context {
	return context.WithValue(ctx, orderAccessKey{}, orderID)
}

// OrderAccessFrom returns the order id named by the verified order access
// token, if any.
func OrderAccessFrom(ctx context.Context) (int, bool) {
	id, ok := ctx.Value(orderAccessKey{}).(int)
	return id, ok
}
