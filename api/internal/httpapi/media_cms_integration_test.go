package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// change product-images-r2 (design D7, spec page-management): with media
// configured, CMS image fields accept only "" or the image domain.
const cmsImageBase = "https://img.example.test"

func putLogo(t *testing.T, e *cmsEnv, url string) (int, string) {
	t.Helper()
	code, _, raw := e.admin1(t, "PUT", e.shopPath("/content"), map[string]any{"content": map[string]any{
		"header": map[string]any{"site_title": "x", "logo_url": url},
	}})
	return code, raw
}

func TestCMSImageFieldRejectsExternalURLWhenMediaConfigured(t *testing.T) {
	e := newCMSEnv(t)
	e.cms.RestrictImageDomain(cmsImageBase)

	code, raw := putLogo(t, e, "https://example.com/a.jpg")
	if code != http.StatusUnprocessableEntity || !strings.Contains(raw, "/header/logo_url") {
		t.Fatalf("external URL: %d %s, want 422 with pointer /header/logo_url", code, raw)
	}
	// Look-alike domain cannot slip through the prefix check (trailing slash).
	if code, _ := putLogo(t, e, cmsImageBase+".evil.test/a.webp"); code != http.StatusUnprocessableEntity {
		t.Errorf("look-alike domain: %d, want 422", code)
	}
	if code, raw := putLogo(t, e, cmsImageBase+"/shops/1/content/abc/large.webp"); code != http.StatusOK {
		t.Errorf("image-domain URL: %d %s, want 200", code, raw)
	}
	if code, raw := putLogo(t, e, ""); code != http.StatusOK {
		t.Errorf("empty: %d %s, want 200", code, raw)
	}
}

func TestCMSImageFieldPageSectionPointer(t *testing.T) {
	e := newCMSEnv(t)
	e.cms.RestrictImageDomain(cmsImageBase)
	code, _, raw := e.admin1(t, "PUT", e.shopPath("/pages/"+strconv.Itoa(e.homeID)), map[string]any{
		"content": map[string]any{"sections": []any{
			map[string]any{"type": "hero", "title": "t", "image": "https://example.com/a.jpg"},
		}},
	})
	if code != http.StatusUnprocessableEntity || !strings.Contains(raw, "/sections/0/image") {
		t.Fatalf("hero external image: %d %s", code, raw)
	}
}

func TestCMSImageFieldUnrestrictedWhenMediaDisabled(t *testing.T) {
	e := newCMSEnv(t) // no RestrictImageDomain: media not configured
	if code, raw := putLogo(t, e, "https://example.com/a.jpg"); code != http.StatusOK {
		t.Fatalf("media disabled must not restrict: %d %s", code, raw)
	}
}

func TestCMSRichTextImageOutsideImageDomainIsRemoved(t *testing.T) {
	e := newCMSEnv(t)
	e.cms.RestrictImageDomain(cmsImageBase)
	code, body, raw := e.admin1(t, "PUT", e.shopPath("/pages/"+strconv.Itoa(e.homeID)), map[string]any{
		"content": map[string]any{"sections": []any{
			map[string]any{"type": "rich_text", "html": `<p>hi</p><img src="https://example.com/x.png"><img src="` + cmsImageBase + `/ok.webp">`},
		}},
	})
	if code != http.StatusOK {
		t.Fatalf("update: %d %s", code, raw)
	}
	html := richTextHTML(t, func() []byte { b, _ := json.Marshal(dig(body, "content_json")); return b }())
	if strings.Contains(html, "example.com") || !strings.Contains(html, "img.example.test/ok.webp") {
		t.Errorf("rich text images not filtered: %s", html)
	}
}
