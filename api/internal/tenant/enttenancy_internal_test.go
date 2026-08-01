package tenant

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/migrate"
)

// notTenantOwned lists the tables that carry a shop_id column and are
// nonetheless **deliberately** absent from tenantOwned. Every one of them is an
// RBAC/platform join table where shop_id scopes a *grant* — "this user has this
// role in this shop" — rather than tenant business data.
//
// Registering them would be actively wrong: the RBAC engine reads them with
// `shop_id = ? OR shop_id IS NULL`, because a platform-level role (shop_id NULL)
// must be unioned into a shop-level snapshot (see admin-commerce-operations
// design D6). The interceptor would rewrite that into `shop_id = ?` and silently
// drop every platform grant.
//
// **This list is not documentation, it is the other half of the check below.**
// Adding a table with a shop_id column fails TestEveryShopScopedTableIsClassified
// until somebody either registers it in tenantOwned or writes it here with a
// reason — which is the decision we want forced, because the symptom of
// skipping it (one shop reading another's rows) is exactly the kind of bug no
// feature test would ever notice.
var notTenantOwned = map[string]string{
	"role_user":       "RBAC grant: shop-scoped role assignment, read with shop_id = ? OR shop_id IS NULL",
	"user_permission": "RBAC grant: shop-scoped direct permission, same NULL-union read",
	"shop_user":       "platform membership: which users belong to which shop",
	"site_shop":       "platform routing: which site/host maps to which shop",
}

// entTypeNames returns every ent entity type name, taken from the generated
// Client's own fields (Cart *CartClient → "Cart"). Reflection rather than a
// literal list, so a renamed or removed entity shows up here instead of rotting.
func entTypeNames(t *testing.T) map[string]bool {
	t.Helper()
	names := make(map[string]bool)
	ct := reflect.TypeOf(ent.Client{})
	for i := 0; i < ct.NumField(); i++ {
		f := ct.Field(i)
		if f.Type.Kind() != reflect.Pointer {
			continue
		}
		if elem := f.Type.Elem().Name(); strings.HasSuffix(elem, "Client") && elem == f.Name+"Client" {
			names[f.Name] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("reflected zero ent types off ent.Client — the generated shape changed and this test is now blind")
	}
	return names
}

// TestTenantOwnedNamesAreRealEntTypes catches the typo case. tenantOwned is
// keyed by the string ent reports from Query.Type()/Mutation.Type(), so a
// misspelled key is not a compile error and not a runtime error — it is simply
// an entity that is never scoped, forever.
func TestTenantOwnedNamesAreRealEntTypes(t *testing.T) {
	valid := entTypeNames(t)
	for name := range tenantOwned {
		if !valid[name] {
			t.Errorf("tenantOwned has %q, which is not an ent entity type — it scopes nothing", name)
		}
	}
}

// TestEveryShopScopedTableIsClassified is the structural guard: every table that
// carries a shop_id column must be either tenant-owned or explicitly exempted.
//
// It compares counts rather than names because ent's generated schema does not
// carry the entity-type ↔ table-name mapping, and inventing one here (snake_case
// plus pluralization) would be a second source of truth that can itself be
// wrong. The blind spot that leaves is narrow and worth stating: adding a
// shop_id table *and* an unrelated bogus tenantOwned entry in the same change
// would balance the count — but the entry would have to name a real ent type
// that has no shop_id column, which the sibling test above does not catch and a
// human reading the diff would.
func TestEveryShopScopedTableIsClassified(t *testing.T) {
	var scoped []string
	for _, tbl := range migrate.Tables {
		for _, col := range tbl.Columns {
			if col.Name == "shop_id" {
				scoped = append(scoped, tbl.Name)
				break
			}
		}
	}
	sort.Strings(scoped)

	if len(scoped) != len(tenantOwned)+len(notTenantOwned) {
		t.Fatalf("%d tables carry shop_id, but %d are registered tenant-owned and %d explicitly exempted (%d classified).\n"+
			"shop_id tables: %v\n"+
			"Register the new one in tenantOwned, or add it to notTenantOwned with the reason it must not be scoped.",
			len(scoped), len(tenantOwned), len(notTenantOwned), len(tenantOwned)+len(notTenantOwned), scoped)
	}

	byName := make(map[string]bool, len(scoped))
	for _, n := range scoped {
		byName[n] = true
	}
	for name := range notTenantOwned {
		if !byName[name] {
			t.Errorf("notTenantOwned exempts %q, which is not a table with a shop_id column (renamed or dropped?)", name)
		}
	}
}

// TestShipmentItemIsTenantOwned pins change partial-shipment's own new table by
// name. The count check above would go red if it were missing, but it would say
// "some table is unclassified" — this says which one, and it is the one whose
// omission would have leaked one shop's parcel contents into another's reads.
func TestShipmentItemIsTenantOwned(t *testing.T) {
	if !tenantOwned["ShipmentItem"] {
		t.Fatal("ShipmentItem must be tenant-owned: shipment_items carries shop_id and holds one shop's parcel contents")
	}
}
