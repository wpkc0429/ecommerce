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
	"Page": true,
	// change headless-content-store slice 2: all three carry a direct shop_id.
	"ContentDocument": true,
	"ContentRevision": true,
	"ContentAuditLog": true,
	// slice 3: the publish hook and (below) schedules carry a direct shop_id.
	"ContentPublishHook": true,
	"ContentSchedule":    true,
	// change retire-hosted-storefront-and-themes (group 1): per-shop content
	// schema versions carry a direct shop_id column.
	"ContentSchema": true,
	// change member-identity-per-shop (design D4): members carries its own
	// shop_id since customer identity became per-shop, so it is tenant-owned
	// like everything else. Back-office paths run without tenant context and
	// must therefore scope members queries by an explicit shop_id.
	"Member":             true,
	"MemberRefreshToken": true,
	// change product-catalog (design D7): all four carry a direct shop_id
	// column, including the ProductCategory join table (defense in depth —
	// see design D2/D7 for why the join table duplicates shop_id).
	"Category":        true,
	"Product":         true,
	"ProductSKU":      true,
	"ProductCategory": true,
	// change product-images-r2: both carry a direct shop_id column.
	"ProductImage": true,
	"MediaUpload":  true,
	// change shopping-cart: both tables carry a direct shop_id column
	// (design D1/D9 — Cart stores shop_id+member_id directly rather than
	// indirecting through ShopMember, mirroring ShopMember's own shape).
	"Cart":     true,
	"CartItem": true,
	// change order-management: both tables carry a direct shop_id column
	// (design D1 — Order stores shop_id+member_id directly, mirroring Cart's
	// own shape).
	"Order":     true,
	"OrderItem": true,
	// change payment-integration: carries a direct shop_id column (design
	// D3). The webhook route intentionally runs without tenant context, so
	// this map only governs the member/admin-facing paths (design D7) —
	// HandleWebhook's own reads/writes are scoped explicitly by primary key
	// and by the shop_id/order_id read off the located row, not by this
	// interceptor.
	"Payment": true,
	// change shipping-logistics: both tables carry a direct shop_id column
	// (design D1/D10 — ShippingMethod mirrors Category's shape, Shipment
	// mirrors Payment's shape).
	"ShippingMethod": true,
	"Shipment":       true,
	// change partial-shipment: shipment_items carries a direct shop_id column
	// (same shape as OrderItem). Every access in the shipping package is
	// already explicitly shop-scoped, but registering it is the default line of
	// defense for whoever reads this table next — and a missing registration's
	// symptom (cross-tenant visibility) is exactly the kind nothing tests.
	"ShipmentItem": true,
	// change member-tiers-and-points: both tables carry a direct shop_id
	// column (design D1 — MemberTier mirrors ShippingMethod's shape,
	// PointTransaction mirrors Payment/Shipment's shape).
	"MemberTier":       true,
	"PointTransaction": true,
	// change storefront-client-auth: both carry a direct shop_id column.
	// Their *resolution* paths (publishable key → shop, origin → registered?)
	// deliberately run before any tenant context exists — they are what
	// establishes it — and scope themselves by the unique key/origin column,
	// so this entry governs the back-office paths only (which, being outside
	// tenant context too, pass an explicit shop_id as always).
	"PublishableKey":    true,
	"ShopAllowedOrigin": true,
	// change checkout-idempotency: carries a direct shop_id column, and the
	// key's uniqueness scope IS (shop_id, idempotency_key) — shop A must never
	// be able to replay shop B's key. idempotency.Service still passes shop_id
	// explicitly on every query (same convention as members): this entry is
	// defense in depth, not the primary guard.
	"CheckoutIdempotencyKey": true,
	// change order-notification-email (design D16): carries a direct shop_id
	// column, mirroring Payment/Shipment's shape. Every access in that change
	// runs outside tenant context (the dispatcher delivers events there) and
	// carries shop_id explicitly, so this entry is the default guard for
	// whatever back-office endpoint reads this table next — and the symptom of
	// forgetting it (one shop's notification rows visible to another) is
	// something no existing test would notice.
	"OrderNotification": true,
	// change ecpay-per-shop-credentials: carries a direct shop_id column
	// (design D5). Every access (credential resolution at InitiatePayment/
	// webhook-verification time, and the admin management endpoint) already
	// passes shop_id explicitly — same defense-in-depth reasoning as
	// PublishableKey/ShopAllowedOrigin above.
	"ShopPaymentProviderCredential": true,
	// change design-history: five tables carry a direct shop_id. Every access in
	// internal/design runs outside tenant context and passes shop_id
	// explicitly (the service credential, not a tenant host, scopes it); the
	// entries are the default guard for whatever reads these tables next.
	"DesignRequest":         true,
	"DesignRequestEvent":    true,
	"DesignScreenshot":      true,
	"DesignSiteState":       true,
	"DesignUntrackedChange": true,
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
