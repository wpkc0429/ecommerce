package design

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designrequest"
	"ksdevworks/ecommerce/api/internal/ent/designsitestate"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/user"
)

// Display names of the design history (follow-up to design D1, privacy note in
// docs/design-history-privacy.md). They are resolved only for the human routes,
// which are guarded by design.view in this shop; the service routes never get
// them. The value is the user's own optional display name; email and phone are
// never used, and a user without one gets a neutral label.
const (
	NameFallbackMember  = "Team member"
	NameFallbackAdmin   = "Platform admin"
	NameDesignService   = "Design service"
	NameSystem          = "System"
	maxDisplayNameRunes = 80
)

// DisplayNames returns a shop-scoped display name for each user id. A user who
// set a display name uses it; otherwise a member of this shop is "Team member"
// and anyone else (a platform admin acting on the shop) is "Platform admin".
// Unknown ids are absent from the map.
func (s *Service) DisplayNames(ctx context.Context, shopID int, ids []int) (map[int]string, error) {
	out := map[int]string{}
	if len(ids) == 0 {
		return out, nil
	}
	uniq := map[int]bool{}
	var list []int
	for _, id := range ids {
		if !uniq[id] {
			uniq[id] = true
			list = append(list, id)
		}
	}
	users, err := s.Client.User.Query().Where(user.IDIn(list...)).Select(user.FieldID, user.FieldDisplayName).All(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.Client.ShopUser.Query().Where(shopuser.ShopIDEQ(shopID), shopuser.UserIDIn(list...)).
		Select(shopuser.FieldUserID).Ints(ctx)
	if err != nil {
		return nil, err
	}
	isMember := map[int]bool{}
	for _, m := range members {
		isMember[m] = true
	}
	for _, u := range users {
		switch {
		case u.DisplayName != nil && strings.TrimSpace(*u.DisplayName) != "":
			out[u.ID] = strings.TrimSpace(*u.DisplayName)
		case isMember[u.ID]:
			out[u.ID] = NameFallbackMember
		default:
			out[u.ID] = NameFallbackAdmin
		}
	}
	return out, nil
}

// CleanDisplayName validates a display name a user sets for themself: trimmed,
// control and bidi characters dropped, at most 80 characters, and nothing that
// redaction would remove (an email address, phone number or card number).
// An empty result clears the name.
func CleanDisplayName(in string) (string, error) {
	v := strings.TrimSpace(SanitizeText(in))
	if utf8.RuneCountInString(v) > maxDisplayNameRunes {
		return "", ErrInvalid("display_name must be at most 80 characters")
	}
	if strings.ContainsAny(v, "\r\n\t") {
		return "", ErrInvalid("display_name must be a single line")
	}
	if red, flags := RedactPII(v); len(flags) > 0 || red != v {
		return "", ErrInvalid("display_name must not contain an email address, phone number or card number")
	}
	return v, nil
}

// RequestExtras are the derived, read-only fields of a request summary.
type RequestExtras struct {
	IsCurrent       bool
	RevertTargetTag string // for kind=revert: the published tag being restored
}

// Extras derives is_current and revert_target_tag for rows of one shop. The
// single source of truth for "current" is design_site_state.current_request_id
// (the same column the state endpoint and the revert check use).
func (s *Service) Extras(ctx context.Context, shopID int, rows []*ent.DesignRequest) (map[uuid.UUID]RequestExtras, error) {
	out := make(map[uuid.UUID]RequestExtras, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	states, err := s.Client.DesignSiteState.Query().Where(designsitestate.ShopIDEQ(shopID)).All(ctx)
	if err != nil {
		return nil, err
	}
	current := map[uuid.UUID]bool{}
	for _, st := range states {
		if st.CurrentRequestID != nil {
			current[*st.CurrentRequestID] = true
		}
	}
	var targets []uuid.UUID
	for _, r := range rows {
		if r.Kind == KindRevert && r.RestoresRequestID != nil {
			targets = append(targets, *r.RestoresRequestID)
		}
	}
	tags := map[uuid.UUID]string{}
	if len(targets) > 0 {
		trs, err := s.Client.DesignRequest.Query().Where(designrequest.ShopIDEQ(shopID), designrequest.IDIn(targets...)).
			Select(designrequest.FieldID, designrequest.FieldPublishedTag).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range trs {
			if t.PublishedTag != nil {
				tags[t.ID] = *t.PublishedTag
			}
		}
	}
	for _, r := range rows {
		e := RequestExtras{IsCurrent: r.PublishedTag != nil && current[r.ID]}
		if r.Kind == KindRevert && r.RestoresRequestID != nil {
			e.RevertTargetTag = tags[*r.RestoresRequestID]
		}
		out[r.ID] = e
	}
	return out, nil
}

// PendingApprovalCounts counts the shop's requests awaiting a person's approval,
// per site and in total.
func (s *Service) PendingApprovalCounts(ctx context.Context, shopID int) (map[string]int, int, error) {
	out := map[string]int{}
	total := 0
	for _, site := range []string{SiteStorefront, SiteAdmin} {
		n, err := s.Client.DesignRequest.Query().Where(
			designrequest.ShopIDEQ(shopID), designrequest.SiteEQ(site), designrequest.StatusEQ(StatusAwaitingApproval)).Count(ctx)
		if err != nil {
			return nil, 0, err
		}
		out[site] = n
		total += n
	}
	return out, total, nil
}
