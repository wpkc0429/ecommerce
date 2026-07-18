package tenant

import (
	"context"

	"entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/intercept"
)

// tenantOwned lists entity types whose rows belong to a single shop and must
// be invisible outside their tenant. Extend when adding tenant-owned tables
// (Phase 2: products, orders, ...).
var tenantOwned = map[string]bool{
	"Page":               true,
	"ShopMember":         true,
	"MemberRefreshToken": true,
	// change product-catalog (design D7): all four carry a direct shop_id
	// column, including the ProductCategory join table (defense in depth —
	// see design D2/D7 for why the join table duplicates shop_id).
	"Category":        true,
	"Product":         true,
	"ProductSKU":      true,
	"ProductCategory": true,
}

// Interceptor forces `shop_id = <ctx shop>` on every query of tenant-owned
// entities whenever a tenant context is present. Requests without tenant
// context (platform admin APIs, CLI tools) are untouched — their scoping is
// the responsibility of the RBAC layer.
func Interceptor() ent.Interceptor {
	return intercept.TraverseFunc(func(ctx context.Context, q intercept.Query) error {
		shopID, ok := ShopID(ctx)
		if !ok {
			return nil
		}
		if tenantOwned[q.Type()] {
			q.WhereP(sql.FieldEQ("shop_id", shopID))
		}
		return nil
	})
}

// Hook enforces the tenant boundary on mutations: creations are stamped with
// the context shop (overriding whatever the caller set — client-supplied shop
// identifiers are never trusted in tenant context), and update/delete
// statements gain a shop_id predicate so cross-tenant rows cannot be touched.
func Hook() ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			shopID, ok := ShopID(ctx)
			if !ok || !tenantOwned[m.Type()] {
				return next.Mutate(ctx, m)
			}
			if m.Op().Is(ent.OpCreate) {
				if err := m.SetField("shop_id", shopID); err != nil {
					return nil, err
				}
			} else if wp, ok := m.(interface {
				WhereP(...func(*sql.Selector))
			}); ok {
				wp.WhereP(sql.FieldEQ("shop_id", shopID))
			}
			return next.Mutate(ctx, m)
		})
	}
}

// Register attaches the tenant enforcement to an ent client. Called from
// database.Open so every client in the codebase is covered.
func Register(c *ent.Client) {
	c.Intercept(Interceptor())
	c.Use(Hook())
}
