package richtext

import (
	"strings"
	"testing"
)

// Malicious vectors: every output must be free of the listed substrings
// (case-insensitive). Spec page-management/Rich text 欄位於寫入時依 allowlist 消毒.
func TestSanitizeRemovesDangerousContent(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		banned []string
	}{
		{"onerror attribute", `<p>hi<img src="https://x.test/a.png" onerror="alert(1)"></p>`, []string{"onerror", "alert"}},
		{"script with content", `<p>a</p><script>alert(1)</script>`, []string{"<script", "alert(1)"}},
		{"style element with content", `<style>body{display:none}</style><p>x</p>`, []string{"<style", "display:none"}},
		{"iframe with content", `<iframe src="https://evil.test">fallback</iframe>`, []string{"iframe", "evil.test", "fallback"}},
		{"javascript href", `<a href="javascript:alert(1)">x</a>`, []string{"javascript", "alert"}},
		{"javascript href mixed case", `<a href="JaVaScRiPt:alert(1)">x</a>`, []string{"javascript", "alert"}},
		{"javascript href entity encoded", `<a href="&#106;avascript:alert(1)">x</a>`, []string{"javascript", "alert", "href"}},
		{"javascript href with whitespace", "<a href=\" java\tscript:alert(1)\">x</a>", []string{"script:", "alert"}},
		{"vbscript href", `<a href="vbscript:msgbox(1)">x</a>`, []string{"vbscript", "msgbox"}},
		{"data url href", `<a href="data:text/html;base64,PHNjcmlwdD4=">x</a>`, []string{"data:"}},
		{"data url img", `<img src="data:image/svg+xml;base64,PHN2Zz4=">`, []string{"data:"}},
		{"http img is not https", `<img src="http://x.test/a.png">`, []string{"http://x.test"}},
		{"svg script", `<svg><script>alert(1)</script></svg>`, []string{"svg", "script", "alert"}},
		{"noscript breakout", `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></noscript>`, []string{"onerror", "alert"}},
		{"style attribute", `<p style="background:url(javascript:alert(1))">x</p>`, []string{"style", "javascript"}},
		{"class and id", `<p class="evil" id="x">x</p>`, []string{"class=", "id="}},
		{"event on allowed tag", `<a href="/ok" onclick="steal()">x</a>`, []string{"onclick", "steal"}},
		{"unclosed tag", `<p>open <b>bold`, []string{"<script"}},
		{"author rel ignored", `<a href="/x" rel="opener">x</a>`, []string{"opener\""}},
		{"form elements", `<form action="https://evil.test"><input name="pw"></form>`, []string{"<form", "<input", "evil.test"}},
		{"h1 is unwrapped", `<h1>大標</h1>`, []string{"<h1"}},
		{"bad target value", `<a href="/x" target="_top">x</a>`, []string{"_top"}},
		{"non numeric colspan", `<table><tr><td colspan="2;x">c</td></tr></table>`, []string{"colspan"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := strings.ToLower(Sanitize(tc.in))
			for _, b := range tc.banned {
				if strings.Contains(out, strings.ToLower(b)) {
					t.Fatalf("output still contains %q:\n in: %s\nout: %s", b, tc.in, out)
				}
			}
		})
	}
}

func TestSanitizeKeepsAllowedFormatting(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"headings and inline", `<h2>標題</h2><p><strong>粗體</strong> 與 <em>斜體</em></p>`, []string{"<h2>標題</h2>", "<strong>粗體</strong>", "<em>斜體</em>"}},
		{"relative link", `<a href="/about">關於</a>`, []string{`<a href="/about">關於</a>`}},
		{"https link with title", `<a href="https://ondo.test/x" title="t">x</a>`, []string{`href="https://ondo.test/x"`, `title="t"`}},
		{"mailto and tel", `<a href="mailto:hi@ondo.test">m</a><a href="tel:+886212345678">t</a>`, []string{`href="mailto:hi@ondo.test"`, `href="tel:+886212345678"`}},
		{"https img", `<img src="https://img.test/a.webp" alt="花" width="300" height="200">`, []string{`src="https://img.test/a.webp"`, `alt="花"`, `width="300"`}},
		{"lists and quote", `<ul><li>a</li></ul><ol><li>b</li></ol><blockquote>q</blockquote><hr>`, []string{"<ul><li>a</li></ul>", "<ol><li>b</li></ol>", "<blockquote>q</blockquote>", "<hr"}},
		{"table", `<table><caption>價目</caption><thead><tr><th scope="col" colspan="2">尺寸</th></tr></thead><tbody><tr><td rowspan="2">S</td><td>3200</td></tr></tbody></table>`,
			[]string{"<table>", "<caption>價目</caption>", `scope="col"`, `colspan="2"`, `rowspan="2"`, "<td>3200</td>"}},
		{"h1 text kept", `<h1>大標</h1>`, []string{"大標"}},
		{"script sibling text kept", `<p>a</p><script>x</script>`, []string{"<p>a</p>"}},
		{"js link text kept", `<a href="javascript:alert(1)">文字</a>`, []string{"文字"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Sanitize(tc.in)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Fatalf("output missing %q:\n in: %s\nout: %s", w, tc.in, out)
				}
			}
		})
	}
}

func TestTargetBlankGetsRel(t *testing.T) {
	out := Sanitize(`<p><a href="https://ondo.test" target="_blank" rel="opener">x</a> <a href="/y">y</a></p>`)
	if !strings.Contains(out, `rel="noopener noreferrer"`) {
		t.Fatalf("target=_blank link must carry rel=noopener noreferrer: %s", out)
	}
	if strings.Count(out, "rel=") != 1 {
		t.Fatalf("only the _blank link gets rel, and the author rel must be gone: %s", out)
	}
}

func TestSanitizeIsIdempotent(t *testing.T) {
	inputs := []string{
		`<p>hi<img src="https://x.test/a.png" onerror="alert(1)"></p>`,
		`<a href="https://ondo.test" target="_blank">x</a>`,
		`<table><tr><td colspan="2">a &amp; b</td></tr></table>`,
		`<p>5 &lt; 6 &amp; "quoted"</p>`,
		`<h1>大標</h1><p>段落</p>`,
	}
	for _, in := range inputs {
		once := Sanitize(in)
		if twice := Sanitize(once); twice != once {
			t.Fatalf("not idempotent:\n once: %s\ntwice: %s", once, twice)
		}
	}
}

func TestEmptyAndPlainText(t *testing.T) {
	if Sanitize("") != "" {
		t.Fatal("empty in, empty out")
	}
	if got := Sanitize("純文字"); got != "純文字" {
		t.Fatalf("plain text changed: %q", got)
	}
}

func TestAllowlistHasNoH1AndHasTables(t *testing.T) {
	r := Rules()
	if _, ok := r.Elements["h1"]; ok {
		t.Fatal("h1 must not be allowlisted (decided: theme owns the page title)")
	}
	for _, tag := range []string{"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption"} {
		if _, ok := r.Elements[tag]; !ok {
			t.Fatalf("table element %s must be allowlisted", tag)
		}
	}
	for tag, attrs := range r.Elements {
		for _, a := range attrs {
			if a == "style" || a == "class" || a == "id" || a == "rel" || strings.HasPrefix(a, "on") {
				t.Fatalf("%s.%s must never be allowlisted", tag, a)
			}
		}
	}
}
