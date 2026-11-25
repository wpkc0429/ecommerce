package design_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"testing"

	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/media"
)

// The retention rule for screenshots (Q1: published 365 days, the rest 30) must
// remove the OBJECT from the private bucket, and the sweep must report what the
// two sides disagree about.
func TestScreenshotPurgeRemovesObjectsAndOrphansAreReported(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	store := media.NewFakeStore()
	t.Cleanup(store.Close)
	f.svc.Store = store
	f.svc.Cfg.ScreenshotsEnabled = true

	img := image.NewRGBA(image.Rect(0, 0, 390, 12))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	manifest := base64.StdEncoding.EncodeToString([]byte(
		`{"tool":"cap","tool_version":"1","dataset":"sandbox-v1","api_origin_class":"sandbox","captured_at":"2026-11-26T00:00:00Z","set_version":1}`))

	shoot := func(shop int, status string, age int) string {
		r := f.request(shop, "admin")
		row, _, err := f.svc.UploadScreenshot(ctx, design.UploadInput{
			ShopID: shop, RequestID: r.ID, Phase: "before", Role: "owner", Viewport: 390, ViewKey: "home",
			Body: buf.Bytes(), Manifest: manifest,
		})
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		f.finish(r, status)
		f.backdate(r.ID, day(age))
		return row.ObjectKey
	}
	oldPublished := shoot(1, "published", 366)
	freshPublished := shoot(2, "published", 364)
	oldFailed := shoot(3, "failed", 31)
	freshFailed := shoot(4, "failed", 29)

	if n := len(store.Keys("shops/")); n != 4 {
		t.Fatalf("objects: %d", n)
	}
	rep, err := f.svc.Orphans(ctx, 0)
	if err != nil || rep.Objects != 4 || rep.Referenced != 4 || len(rep.Orphans) != 0 || rep.Missing != 0 {
		t.Fatalf("clean state: %+v %v", rep, err)
	}

	p := f.purger(design.DeleterOf(store))
	pc := design.DefaultPurgeConfig()
	if _, err := p.Purge(ctx, pc, true); err != nil {
		t.Fatal(err)
	}
	if store.Has(oldPublished) || store.Has(oldFailed) {
		t.Fatal("objects past retention were not deleted from the bucket")
	}
	if !store.Has(freshPublished) || !store.Has(freshFailed) {
		t.Fatal("objects inside retention were deleted")
	}

	// A stray object (an upload whose row was lost) and a row without its object.
	store.Seed("shops/9/design/00000000-0000-0000-0000-000000000000/stray.png", []byte("x"), "image/png")
	if err := store.Delete(ctx, freshFailed); err != nil {
		t.Fatal(err)
	}
	rep, err = f.svc.Orphans(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0] != "shops/9/design/00000000-0000-0000-0000-000000000000/stray.png" || rep.Missing != 1 || rep.Referenced != 1 {
		t.Fatalf("orphan report: %+v", rep)
	}
	// Scoped to one shop it ignores the others.
	if rep, _ = f.svc.Orphans(ctx, 2); len(rep.Orphans) != 0 || rep.Missing != 0 || rep.Referenced != 1 {
		t.Fatalf("shop-scoped report: %+v", rep)
	}
}
