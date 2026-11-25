package design

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designscreenshot"
)

// Slice 1 has no upload path; the screenshots table is read only to decide
// whether a request may be submitted (design D4, D18: no after-screenshots, no
// approval). Slice 2 writes the rows.

type slot struct {
	Role     string
	Viewport int16
	View     string
}

// screenshotsComplete requires the before set (taken at base_sha) and the after
// set (taken at head_sha) to exist, to cover exactly the same slots, and to come
// from the same view-set version.
func (s *Service) screenshotsComplete(ctx context.Context, r *ent.DesignRequest) error {
	rows, err := s.Client.DesignScreenshot.Query().
		Where(designscreenshot.RequestIDEQ(r.ID), designscreenshot.ShopIDEQ(r.ShopID), designscreenshot.PurgedAtIsNil()).
		Order(ent.Asc(designscreenshot.FieldAttempt)).All(ctx)
	if err != nil {
		return err
	}
	latest := map[string]map[slot]*ent.DesignScreenshot{"before": {}, "after": {}}
	for _, row := range rows { // attempts ascend, so the last write per slot is the newest
		latest[row.Phase][slot{row.Role, row.Viewport, row.ViewKey}] = row
	}
	incomplete := func(why string) error {
		return conflict("screenshots_incomplete", why)
	}
	if len(latest["before"]) == 0 || len(latest["after"]) == 0 {
		return incomplete("both the before and the after screenshot sets are required")
	}
	if len(latest["before"]) != len(latest["after"]) {
		return incomplete("the before and after sets cover different views")
	}
	versions := map[int]bool{}
	for sl, b := range latest["before"] {
		a, ok := latest["after"][sl]
		if !ok {
			return incomplete(fmt.Sprintf("no after screenshot for %s/%d/%s", sl.Role, sl.Viewport, sl.View))
		}
		if b.CommitSha != r.BaseSha {
			return incomplete("a before screenshot was not taken at the base commit")
		}
		if a.CommitSha != r.HeadSha {
			return incomplete("an after screenshot was not taken at the current head")
		}
		versions[setVersion(b.CaptureManifest)] = true
		versions[setVersion(a.CaptureManifest)] = true
	}
	if len(versions) != 1 {
		vs := make([]int, 0, len(versions))
		for v := range versions {
			vs = append(vs, v)
		}
		sort.Ints(vs)
		return incomplete(fmt.Sprintf("the screenshots come from different view-set versions %v", vs))
	}
	return nil
}

func setVersion(manifest json.RawMessage) int {
	var m struct {
		SetVersion int `json:"set_version"`
	}
	_ = json.Unmarshal(manifest, &m)
	return m.SetVersion
}
