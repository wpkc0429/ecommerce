package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/events"
)

// change cms-rich-text-sanitization: spec page-management/Rich text 欄位於寫入時依
// allowlist 消毒 and content-rendering (bundle carries the sanitized value).

const xssRichText = `<p>hi<img src="https://img.test/a.png" onerror="window.__xss=1"></p><script>window.__xss=2</script><h1>大標</h1><table><tr><td colspan="2">x</td></tr></table>`

func assertSanitized(t *testing.T, where, html string) {
	t.Helper()
	low := strings.ToLower(html)
	for _, banned := range []string{"onerror", "<script", "__xss", "<h1"} {
		if strings.Contains(low, banned) {
			t.Fatalf("%s still contains %q: %s", where, banned, html)
		}
	}
	for _, kept := range []string{"<p>hi", `src="https://img.test/a.png"`, "大標", `colspan="2"`} {
		if !strings.Contains(html, kept) {
			t.Fatalf("%s lost allowed content %q: %s", where, kept, html)
		}
	}
}

// richTextHTML extracts the first rich_text section's html from a stored
// payload. Assertions must run on the parsed value: the raw bytes are JSON
// (quotes escaped) and jsonb re-serializes them, so byte comparison is
// meaningless.
func richTextHTML(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var c struct {
		Sections []map[string]any `json:"sections"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	for _, s := range c.Sections {
		if s["type"] == "rich_text" {
			h, _ := s["html"].(string)
			return h
		}
	}
	t.Fatalf("no rich_text section in %s", raw)
	return ""
}

func TestRichTextSanitizedOnPageWriteAndRender(t *testing.T) {
	e := newCMSEnv(t)
	content := map[string]any{"sections": []any{
		map[string]any{"type": "hero", "title": "<b>純文字標題</b>"},
		map[string]any{"type": "rich_text", "html": xssRichText},
	}}

	// Update (draft): response carries the stored, sanitized value.
	code, body, raw := e.admin1(t, "PUT", e.shopPath(fmt.Sprintf("/pages/%d", e.homeID)),
		map[string]any{"content": content})
	if code != 200 {
		t.Fatalf("update: %d %s", code, raw)
	}
	sections, _ := dig(body, "content_json", "sections").([]any)
	if len(sections) != 2 {
		t.Fatalf("response sections: %s", raw)
	}
	assertSanitized(t, "update response", sections[1].(map[string]any)["html"].(string))
	// x-editor:text is not HTML and is stored verbatim.
	if sections[0].(map[string]any)["title"] != "<b>純文字標題</b>" {
		t.Fatalf("text field must be untouched: %v", sections[0])
	}

	// DB holds the sanitized value.
	stored := e.client.Page.GetX(context.Background(), e.homeID)
	assertSanitized(t, "stored content_json", richTextHTML(t, stored.ContentJSON))

	// Publish → render bundle serves the sanitized value.
	if code, _, raw := e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", e.homeID)), nil); code != 200 {
		t.Fatalf("publish: %d %s", code, raw)
	}
	rcode, bundle, _ := e.renderPage(t, "/")
	if rcode != 200 {
		t.Fatalf("render: %d", rcode)
	}
	rs, _ := dig(bundle, "page", "content", "sections").([]any)
	if len(rs) != 2 {
		t.Fatalf("bundle sections: %v", rs)
	}
	assertSanitized(t, "render bundle", rs[1].(map[string]any)["html"].(string))
}

func TestRichTextSanitizedOnPageCreate(t *testing.T) {
	e := newCMSEnv(t)
	code, body, raw := e.admin1(t, "POST", e.shopPath("/pages"), map[string]any{
		"type_key": "landing_page", "title": "促銷", "slug": "promo-xss",
		"content": map[string]any{"sections": []any{
			map[string]any{"type": "rich_text", "html": `<a href="JaVaScRiPt:alert(1)" target="_blank">x</a>`},
		}},
	})
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	html, _ := dig(body, "content_json", "sections").([]any)[0].(map[string]any)["html"].(string)
	if strings.Contains(strings.ToLower(html), "javascript") || strings.Contains(html, "href") {
		t.Fatalf("javascript href must be dropped: %s", html)
	}
}

// The starter config_schema has no rich text field, so the test extends the
// seeded theme with one — exactly what a theme update could do in production.
func TestRichTextSanitizedOnShopContent(t *testing.T) {
	e := newCMSEnv(t)
	ctx := context.Background()
	sh := e.client.Shop.GetX(ctx, e.shopID)
	theme := e.client.Theme.GetX(ctx, *sh.ThemeID)
	var schema map[string]any
	if err := json.Unmarshal(theme.ConfigSchema, &schema); err != nil {
		t.Fatal(err)
	}
	header := schema["properties"].(map[string]any)["header"].(map[string]any)
	header["properties"].(map[string]any)["notice"] = map[string]any{
		"type": "string", "default": "", "x-editor": "richtext",
	}
	newSchema, _ := json.Marshal(schema)
	e.client.Theme.UpdateOneID(theme.ID).SetConfigSchema(newSchema).ExecX(ctx)

	code, _, raw := e.admin1(t, "PUT", e.shopPath("/content"), map[string]any{"content": map[string]any{
		"header": map[string]any{"site_title": "<i>招牌</i>", "notice": xssRichText},
	}})
	if code != 200 {
		t.Fatalf("update content: %d %s", code, raw)
	}
	stored := e.client.Shop.GetX(ctx, e.shopID)
	var c map[string]any
	_ = json.Unmarshal(stored.ContentJSON, &c)
	hdr := c["header"].(map[string]any)
	assertSanitized(t, "shop content notice", hdr["notice"].(string))
	if hdr["site_title"] != "<i>招牌</i>" {
		t.Fatalf("text field must be untouched: %v", hdr["site_title"])
	}
}

// spec page-management/既有內容可一次性回填消毒.
func TestRichTextBackfill(t *testing.T) {
	e := newCMSEnv(t)
	ctx := context.Background()

	// Plant pre-change content directly (bypassing the write path).
	dirty, _ := json.Marshal(map[string]any{"sections": []any{
		map[string]any{"type": "rich_text", "html": xssRichText},
	}})
	e.client.Page.UpdateOneID(e.homeID).
		SetContentJSON(dirty).SetPublishedJSON(dirty).SetStatus(1).
		ExecX(ctx)

	var got []events.Event
	dispatcher := events.NewDispatcher(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	dispatcher.Subscribe(func(_ context.Context, ev events.Event) { got = append(got, ev) })
	svc := &cms.Service{Client: e.client, Dispatcher: dispatcher}

	// Dry run: reports, writes nothing, publishes nothing.
	sum, err := svc.BackfillRichText(ctx, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Changed != 2 { // home content_json + published_json
		t.Fatalf("dry run changed=%d, want 2 (%+v)", sum.Changed, sum)
	}
	if p := e.client.Page.GetX(ctx, e.homeID); !strings.Contains(richTextHTML(t, p.ContentJSON), "onerror") ||
		!strings.Contains(richTextHTML(t, p.PublishedJSON), "onerror") {
		t.Fatal("dry run must not write")
	}
	if len(got) != 0 {
		t.Fatalf("dry run must not publish events: %v", got)
	}

	// Apply: both copies sanitized; the render cache is invalidated by event.
	var reported []cms.BackfillItem
	sum, err = svc.BackfillRichText(ctx, true, func(it cms.BackfillItem) { reported = append(reported, it) })
	if err != nil || sum.Changed != 2 {
		t.Fatalf("apply: sum=%+v err=%v", sum, err)
	}
	p := e.client.Page.GetX(ctx, e.homeID)
	assertSanitized(t, "backfilled content_json", richTextHTML(t, p.ContentJSON))
	assertSanitized(t, "backfilled published_json", richTextHTML(t, p.PublishedJSON))
	foundPublish := false
	for _, ev := range got {
		if pp, ok := ev.(events.PagePublished); ok && pp.ShopID == e.shopID && pp.Slug == "home" {
			foundPublish = true
		}
	}
	if !foundPublish {
		t.Fatalf("apply must publish PagePublished for the changed published_json: %v", got)
	}
	// The report never carries content.
	for _, it := range reported {
		if strings.Contains(fmt.Sprintf("%+v", it), "xss") {
			t.Fatalf("report leaked content: %+v", it)
		}
	}

	// Idempotent.
	sum, err = svc.BackfillRichText(ctx, true, nil)
	if err != nil || sum.Changed != 0 {
		t.Fatalf("second run: sum=%+v err=%v", sum, err)
	}
}
