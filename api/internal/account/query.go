package account

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/roleuser"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/user"
)

// Summary is the list/detail projection of an account. It deliberately has no
// password field of any kind: HasPassword is the only thing derived from it.
type Summary struct {
	ID          int
	Email       string
	Status      int16
	HasPassword bool
	CreatedAt   time.Time
}

func summarize(u *ent.User) Summary {
	return Summary{
		ID: u.ID, Email: u.Email, Status: u.Status,
		HasPassword: u.PasswordHash != nil, CreatedAt: u.CreatedAt,
	}
}

// RoleRef names a role.
type RoleRef struct {
	ID   int
	Name string
}

// Membership is one shop an account belongs to, with its roles there.
type Membership struct {
	ShopID   int
	ShopName string
	Roles    []RoleRef
}

// Detail is an account with its memberships and platform roles.
type Detail struct {
	Summary
	Memberships   []Membership
	PlatformRoles []RoleRef
}

// Page is one page of accounts.
type Page struct {
	Accounts []Summary
	Total    int
	Page     int
	PageSize int
}

// List returns accounts ordered by id; q is an email substring or an id.
func (s *Service) List(ctx context.Context, q string, page, pageSize int) (*Page, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	query := s.Client.User.Query()
	if q = strings.ToLower(strings.TrimSpace(q)); q != "" {
		if id, err := strconv.Atoi(q); err == nil && id > 0 {
			query = query.Where(user.Or(user.IDEQ(id), user.EmailContains(q)))
		} else {
			query = query.Where(user.EmailContains(q))
		}
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: count: %w", err)
	}
	rows, err := query.Order(user.ByID()).Limit(pageSize).Offset((page - 1) * pageSize).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: list: %w", err)
	}
	out := &Page{Total: total, Page: page, PageSize: pageSize, Accounts: make([]Summary, 0, len(rows))}
	for _, u := range rows {
		out.Accounts = append(out.Accounts, summarize(u))
	}
	return out, nil
}

// Get returns one account with memberships and roles.
func (s *Service) Get(ctx context.Context, id int) (*Detail, error) {
	u, err := s.Client.User.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("account: get: %w", err)
	}
	d := &Detail{Summary: summarize(u), Memberships: []Membership{}, PlatformRoles: []RoleRef{}}

	members, err := s.Client.ShopUser.Query().
		Where(shopuser.UserIDEQ(id)).WithShop().Order(shopuser.ByShopID()).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: memberships: %w", err)
	}
	assignments, err := s.Client.RoleUser.Query().Where(roleuser.UserIDEQ(id)).WithRole().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: roles: %w", err)
	}
	byShop := map[int][]RoleRef{}
	for _, a := range assignments {
		if a.Edges.Role == nil {
			continue
		}
		ref := RoleRef{ID: a.RoleID, Name: a.Edges.Role.Name}
		if a.ShopID == nil {
			d.PlatformRoles = append(d.PlatformRoles, ref)
		} else {
			byShop[*a.ShopID] = append(byShop[*a.ShopID], ref)
		}
	}
	for _, m := range members {
		mem := Membership{ShopID: m.ShopID, Roles: byShop[m.ShopID]}
		if mem.Roles == nil {
			mem.Roles = []RoleRef{}
		}
		if m.Edges.Shop != nil {
			mem.ShopName = m.Edges.Shop.Name
		}
		d.Memberships = append(d.Memberships, mem)
	}
	return d, nil
}
