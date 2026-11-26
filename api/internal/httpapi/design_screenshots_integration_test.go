package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/media"
)

// Screenshot upload and signed reads (change design-history, slice 2; design
// D5, D6, D11). The private bucket is a media.FakeStore.

// shotPNG builds a w x h PNG; seed varies the pixels so two images differ.
func shotPNG(t *testing.T, w, h int, seed uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x) ^ seed, uint8(y), seed, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withTextChunk inserts a tEXt chunk (an author comment) right after IHDR.
func withTextChunk(src []byte, text string) []byte {
	const ihdrEnd = 8 + 25 // signature + length(4)+type(4)+data(13)+crc(4)
	data := append([]byte("Comment\x00"), text...)
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	chunk = append(chunk, "tEXt"...)
	chunk = append(chunk, data...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(append([]byte("tEXt"), data...)))
	out := append([]byte{}, src[:ihdrEnd]...)
	out = append(out, chunk...)
	return append(out, src[ihdrEnd:]...)
}

func manifestHeader(dataset, origin string) string {
	m := fmt.Sprintf(`{"tool":"design-capture","tool_version":"1","dataset":%q,"api_origin_class":%q,"captured_at":"2026-11-26T10:00:00Z","set_version":1,"extra":"dropped"}`, dataset, origin)
	return base64.StdEncoding.EncodeToString([]byte(m))
}

func (e *designEnv) upload(key, rid, phase, role string, viewport int, view string, body []byte, hdr ...string) dresp {
	e.t.Helper()
	path := e.svcPath(e.shopA, fmt.Sprintf("/requests/%s/screenshots/%s/%s/%d/%s", rid, phase, role, viewport, view))
	req := httptest.NewRequest("PUT", path, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("X-Capture-Manifest", manifestHeader("sandbox-v1", "sandbox"))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return e.send(req)
}

func (e *designEnv) retentionExec(q string, args ...any) {
	e.t.Helper()
	tx, err := e.sqlDB.Begin()
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT set_config('design.retention', 'on', true)`); err != nil {
		e.t.Fatal(err)
	}
	if _, err := tx.Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatal(err)
	}
}

func newShotEnv(t *testing.T) (*designEnv, *media.FakeStore) {
	e := newDesignEnv(t)
	store := media.NewFakeStore()
	t.Cleanup(store.Close)
	e.svc.Store = store
	return e, store
}

func (e *designEnv) newAdminRequest() string {
	e.t.Helper()
	rid := id(e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "calmer orders"), 201, "create"))
	e.mustCode(e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/requests/"+rid+"/head"), map[string]any{"head_sha": shaHead, "diff_summary": map[string]any{
		"files_changed": 1, "additions": 1, "deletions": 0, "files": []map[string]any{{"path": "admin/design/theme.css", "status": "modified", "additions": 1, "deletions": 0}}}}), 200, "head")
	return rid
}

func TestScreenshotUploadStoresReencodedPNGAndReplays(t *testing.T) {
	e, store := newShotEnv(t)
	rid := e.newAdminRequest()
	raw := withTextChunk(shotPNG(t, 390, 40, 1), "gps=25.03,121.56 secret-author")
	sum := sha256.Sum256(raw)

	r := e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "orders", raw, "X-Content-SHA256", hex.EncodeToString(sum[:])), 201, "upload")
	e.val.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/requests/{requestID}/screenshots/{phase}/{role}/{viewport}/{viewKey}", "PUT", 201, []byte(r.Raw))
	if r.Body["attempt"] != float64(1) || r.Body["commit_sha"] != shaBase || r.Body["width"] != float64(390) || r.Body["height"] != float64(40) {
		t.Fatalf("metadata: %s", r.Raw)
	}
	if strings.Contains(r.Raw, "object_key") || strings.Contains(r.Raw, "shops/") {
		t.Fatalf("the storage key leaked: %s", r.Raw)
	}
	keys := store.Keys("shops/")
	if len(keys) != 1 || !strings.HasPrefix(keys[0], fmt.Sprintf("shops/%d/design/%s/", e.shopA, rid)) || !strings.HasSuffix(keys[0], ".png") {
		t.Fatalf("keys: %v", keys)
	}
	body, ct, cc, _ := store.Object(keys[0])
	if ct != "image/png" || cc != "private, no-store" {
		t.Fatalf("object headers: %q %q", ct, cc)
	}
	if bytes.Contains(body, []byte("secret-author")) || bytes.Contains(body, []byte("tEXt")) {
		t.Fatal("metadata survived the re-encode")
	}
	stored := sha256.Sum256(body)
	if r.Body["sha256"] != hex.EncodeToString(stored[:]) {
		t.Fatalf("sha256 must be of the stored bytes: %s", r.Raw)
	}
	if m, _ := r.Body["capture_manifest"].(map[string]any); m["extra"] != nil || m["dataset"] != "sandbox-v1" {
		t.Fatalf("manifest: %s", r.Raw)
	}

	// Same bytes again: 200, no new row, no new object.
	again := e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "orders", raw), 200, "replay")
	e.val.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/requests/{requestID}/screenshots/{phase}/{role}/{viewport}/{viewKey}", "PUT", 200, []byte(again.Raw))
	if again.Body["id"] != r.Body["id"] || again.Header.Get("Idempotency-Replayed") != "true" || len(store.Keys("shops/")) != 1 {
		t.Fatalf("replay: %s keys=%v", again.Raw, store.Keys("shops/"))
	}
	// Different bytes for the slot: a new attempt, the old one is kept.
	next := e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "orders", shotPNG(t, 390, 40, 9)), 201, "attempt 2")
	if next.Body["attempt"] != float64(2) || len(store.Keys("shops/")) != 2 {
		t.Fatalf("attempt 2: %s", next.Raw)
	}
	af := e.mustCode(e.upload(e.keyA, rid, "after", "owner", 390, "orders", shotPNG(t, 390, 40, 2)), 201, "after")
	if af.Body["commit_sha"] != shaHead {
		t.Fatalf("after must be at the head: %s", af.Raw)
	}
}

func TestScreenshotUploadValidation(t *testing.T) {
	e, store := newShotEnv(t)
	rid := e.newAdminRequest()
	good := shotPNG(t, 390, 20, 3)
	up := func(body []byte, hdr ...string) dresp { return e.upload(e.keyA, rid, "before", "owner", 390, "home", body, hdr...) }

	e.wantErr(up([]byte("not a png at all")), 422, "invalid_screenshot", "garbage")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "home", shotPNG(t, 380, 20, 1)), 422, "invalid_screenshot", "width != viewport")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 1280, "home", good), 422, "invalid_screenshot", "390 wide image on the 1280 slot")
	e.wantErr(up(shotPNG(t, 390, 10_001, 1)), 422, "invalid_screenshot", "too tall")
	e.wantErr(up(good, "X-Content-SHA256", strings.Repeat("0", 64)), 422, "invalid_screenshot", "checksum mismatch")
	e.wantErr(up(good, "X-Capture-Manifest", ""), 422, "invalid_screenshot", "no manifest")
	e.wantErr(up(good, "X-Capture-Manifest", "%%%"), 422, "invalid_screenshot", "bad manifest")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 391, "home", good), 422, "validation_failed", "viewport")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "Bad_View", good), 422, "validation_failed", "view key")
	e.wantErr(e.upload(e.keyA, rid, "during", "owner", 390, "home", good), 422, "validation_failed", "phase")
	big := append(append([]byte{}, good...), bytes.Repeat([]byte{0}, 2<<20)...)
	e.wantErr(up(big), 413, "invalid_screenshot", "2 MiB")
	if r := up(good, "Content-Type", "image/jpeg"); r.Code != 415 {
		t.Fatalf("content type: %d %s", r.Code, r.Raw)
	}
	if len(store.Keys("shops/")) != 0 || e.client.DesignScreenshot.Query().CountX(context.Background()) != 0 {
		t.Fatal("a refused upload left something behind")
	}

	// Another shop's credential, an unknown request: 404; no credential: 401.
	if c := e.machine(e.keyB, "PUT", e.svcPath(e.shopA, "/requests/"+rid+"/screenshots/before/owner/390/home"), nil).Code; c != 404 {
		t.Fatalf("cross-shop credential: %d", c)
	}
	e.wantErr(e.upload(e.keyA, uuid.NewString(), "before", "owner", 390, "home", good), 404, "not_found", "unknown request")
	if r := e.upload("", rid, "before", "owner", 390, "home", good); r.Code != 401 {
		t.Fatalf("anonymous upload: %d", r.Code)
	}
}

func TestScreenshotSandboxOnlyAdminRule(t *testing.T) {
	e, _ := newShotEnv(t)
	rid := e.newAdminRequest()
	img := shotPNG(t, 390, 20, 4)
	live := manifestHeader("public-live", "public")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "orders", img, "X-Capture-Manifest", live), 422, "live_data_capture_refused", "admin from live")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "orders", img, "X-Capture-Manifest", manifestHeader("sandbox-v1", "public")), 422, "live_data_capture_refused", "sandbox dataset on a public origin")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "orders", img, "X-Capture-Manifest", manifestHeader("", "sandbox")), 422, "live_data_capture_refused", "no dataset")
	e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "orders", img), 201, "admin from sandbox")

	// Storefront: public data only, anonymous only.
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/failed"), map[string]any{"code": "service_error"}), 200, "free the site")
	sf := id(e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "storefront", "bigger photos"), 201, "storefront request"))
	e.mustCode(e.upload(e.keyA, sf, "before", "anon", 390, "home", img, "X-Capture-Manifest", live), 201, "storefront from live public data")
	e.mustCode(e.upload(e.keyA, sf, "before", "anon", 1280, "home", shotPNG(t, 1280, 20, 1)), 201, "storefront from sandbox")
	e.wantErr(e.upload(e.keyA, sf, "before", "owner", 390, "home", img), 422, "live_data_capture_refused", "storefront logged in")
	e.wantErr(e.upload(e.keyA, sf, "before", "anon", 390, "cart", img, "X-Capture-Manifest", manifestHeader("live", "public")), 422, "live_data_capture_refused", "unknown dataset")
}

func TestScreenshotQuotas(t *testing.T) {
	e, store := newShotEnv(t)
	rid := e.newAdminRequest()
	ctx := context.Background()
	img := shotPNG(t, 390, 20, 5)

	// Per shop: the bytes already stored plus this image must fit.
	e.svc.Cfg.ShopQuotaBytes = 100
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "home", img), 409, "quota_exceeded", "shop quota")
	e.svc.Cfg.ShopQuotaBytes = 0
	if len(store.Keys("shops/")) != 0 {
		t.Fatal("an over-quota upload wrote an object")
	}

	// Per request: 80 retained slots. A re-shoot of an existing slot does not
	// consume slot budget; a new slot trips slot_cap_exceeded.
	r := uuid.MustParse(rid)
	for i := 0; i < 80; i++ {
		e.client.DesignScreenshot.Create().SetShopID(e.shopA).SetRequestID(r).SetPhase("before").SetSite("admin").
			SetViewKey(fmt.Sprintf("v%d", i)).SetViewport(390).SetRole("owner").SetCommitSha(shaBase).
			SetObjectKey(fmt.Sprintf("k%d", i)).SetByteSize(10).SetWidth(390).SetHeight(10).SetSha256(strings.Repeat("d", 64)).SaveX(ctx)
	}
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "home", img), 409, "slot_cap_exceeded", "81st slot")
	e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "v3", img), 201, "re-shoot of a full slot set")
	e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "v3", shotPNG(t, 390, 20, 9)), 201, "second re-shoot")
	// A purged slot is still a slot (its history stays on the request).
	e.retentionExec(`UPDATE design_screenshots SET purged_at = now() WHERE request_id = $1 AND view_key = 'v0'`, r)
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "home", img), 409, "slot_cap_exceeded", "purged slot still counts")

	// Per request: every attempt counts toward the wider attempt cap.
	e.svc.Cfg.MaxScreenshotAttempts = 83 // 80 seeded + 2 re-shoots + 1 more
	e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "v3", shotPNG(t, 390, 20, 10)), 201, "last attempt")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "v3", shotPNG(t, 390, 20, 11)), 409, "attempt_cap_exceeded", "attempt cap")
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "v4", img), 409, "attempt_cap_exceeded", "attempt cap, other slot")
	e.svc.Cfg.MaxScreenshotAttempts = 0

	// Per request: 40 MiB (the stored size of existing rows is what counts).
	e.retentionExec(`UPDATE design_screenshots SET purged_at = now() WHERE request_id = $1`, r)
	for i := 0; i < 20; i++ { // 20 x 2 MiB = 40 MiB, the whole budget
		e.client.DesignScreenshot.Create().SetShopID(e.shopA).SetRequestID(r).SetPhase("before").SetSite("admin").
			SetViewKey(fmt.Sprintf("huge%d", i)).SetViewport(390).SetRole("owner").SetCommitSha(shaBase).SetObjectKey(fmt.Sprintf("huge%d", i)).
			SetByteSize(2 << 20).SetWidth(390).SetHeight(10).SetSha256(strings.Repeat("e", 64)).SaveX(ctx)
	}
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "v3", shotPNG(t, 390, 20, 12)), 409, "quota_exceeded", "40 MiB")
}

func TestScreenshotSignedReads(t *testing.T) {
	e, store := newShotEnv(t)
	rid := e.newAdminRequest()
	e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "orders", shotPNG(t, 390, 20, 1)), 201, "before")
	e.mustCode(e.upload(e.keyA, rid, "before", "owner", 390, "orders", shotPNG(t, 390, 20, 2)), 201, "before attempt 2")
	e.mustCode(e.upload(e.keyA, rid, "after", "owner", 390, "orders", shotPNG(t, 390, 20, 3)), 201, "after")
	path := e.adminPath(e.shopA, "/requests/"+rid+"/screenshots")

	r := e.mustCode(e.human(e.ownerA, "GET", path, nil), 200, "list")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests/{requestID}/screenshots", "GET", 200, []byte(r.Raw))
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", r.Header.Get("Cache-Control"))
	}
	list := r.Body["screenshots"].([]any)
	if len(list) != 2 { // newest attempt per slot
		t.Fatalf("want 2 slots, got %d: %s", len(list), r.Raw)
	}
	first := list[0].(map[string]any) // after/owner/390/orders sorts first
	if first["url"] == nil || first["url_expires_at"] == nil {
		t.Fatalf("a url is expected: %v", first)
	}
	if strings.Contains(r.Raw, "shops/") {
		t.Fatalf("object key leaked: %s", r.Raw)
	}
	for _, x := range list {
		if x.(map[string]any)["phase"] == "before" && x.(map[string]any)["attempt"] != float64(2) {
			t.Fatalf("not the newest attempt: %v", x)
		}
	}
	resp, err := http.Get(first["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.HasPrefix(got, []byte("\x89PNG")) {
		t.Fatalf("signed url: %d", resp.StatusCode)
	}

	all := e.mustCode(e.human(e.ownerA, "GET", path+"?all=true", nil), 200, "all")
	if n := len(all.Body["screenshots"].([]any)); n != 3 {
		t.Fatalf("all=true: %d", n)
	}
	one := e.mustCode(e.human(e.ownerA, "GET", path+"?phase=after", nil), 200, "phase")
	if n := len(one.Body["screenshots"].([]any)); n != 1 {
		t.Fatalf("phase=after: %d", n)
	}
	e.wantErr(e.human(e.ownerA, "GET", path+"?phase=x", nil), 422, "validation_failed", "bad phase")

	// Who may read: owner and editor (design.view); nobody else.
	e.mustCode(e.human(e.editorA, "GET", path, nil), 200, "editor")
	for _, u := range []int{e.staffA, e.outsiderA, e.ownerB} {
		if c := e.human(u, "GET", path, nil).Code; c != 403 && c != 404 {
			t.Fatalf("user %d may not read: %d", u, c)
		}
	}
	if c := e.machine(e.keyA, "GET", path, nil).Code; c != 401 {
		t.Fatalf("a service credential must not read screenshots: %d", c)
	}

	// Purged rows are listed without a url.
	e.retentionExec(`UPDATE design_screenshots SET purged_at = now() WHERE request_id = $1`, uuid.MustParse(rid))
	pr := e.mustCode(e.human(e.ownerA, "GET", path, nil), 200, "purged")
	if x := pr.Body["screenshots"].([]any)[0].(map[string]any); x["url"] != nil || x["purged_at"] == nil {
		t.Fatalf("purged entry: %v", x)
	}
	// Neither keys nor signed URLs reach the log.
	logs := e.buf.String()
	for _, k := range store.Keys("shops/") {
		if strings.Contains(logs, k) {
			t.Fatal("an object key reached the log")
		}
	}
	if strings.Contains(logs, strings.TrimPrefix(first["url"].(string), "http://")) {
		t.Fatal("a signed url reached the log")
	}
}

func TestScreenshotUploadThenSubmit(t *testing.T) {
	e, _ := newShotEnv(t)
	rid := id(e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "calmer"), 201, "create"))
	e.toPreviewing(rid)
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/submit"), nil), 409, "screenshots_incomplete", "no shots")
	for _, vp := range []int{390, 1280} {
		e.mustCode(e.upload(e.keyA, rid, "before", "owner", vp, "home", shotPNG(t, vp, 30, 1)), 201, "before")
		e.mustCode(e.upload(e.keyA, rid, "after", "owner", vp, "home", shotPNG(t, vp, 30, 2)), 201, "after")
	}
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/submit"), nil), 200, "submit")
	// Awaiting approval: the set is frozen.
	e.wantErr(e.upload(e.keyA, rid, "after", "owner", 390, "home", shotPNG(t, 390, 30, 7)), 409, "invalid_state", "upload after submit")
}

func TestScreenshotUploadWithoutStorageIs503AfterAuth(t *testing.T) {
	e := newDesignEnv(t) // no Store
	rid := e.newAdminRequest()
	e.wantErr(e.upload(e.keyA, rid, "before", "owner", 390, "home", shotPNG(t, 390, 10, 1)), 503, "design_storage_not_configured", "no store")
	if c := e.upload("dsk_wrong", rid, "before", "owner", 390, "home", nil).Code; c != 401 {
		t.Fatalf("auth comes before the 503: %d", c)
	}
}
