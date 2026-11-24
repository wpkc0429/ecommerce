package richtext

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// SanitizeWithImagePrefixes is Sanitize plus the image-domain rule of change
// product-images-r2 (design D7): when prefixes is non-empty, every <img> whose
// src does not start with one of them is removed. Prefixes MUST end in "/" so
// "https://img.example.test" cannot be satisfied by
// "https://img.example.test.evil.test/x.webp". Like every other rule here it is
// a rewrite, never a rejection.
func SanitizeWithImagePrefixes(in string, prefixes []string) string {
	out := Sanitize(in)
	if len(prefixes) == 0 || !strings.Contains(out, "<img") {
		return out
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(out), ctx)
	if err != nil {
		return out
	}
	srcOf := func(n *html.Node) string {
		for _, at := range n.Attr {
			if at.Key == "src" {
				return at.Val
			}
		}
		return ""
	}
	allowed := func(src string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(src, p) {
				return true
			}
		}
		return false
	}
	var strip func(n *html.Node)
	strip = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.ElementNode && c.Data == "img" && !allowed(srcOf(c)) {
				n.RemoveChild(c)
				c = next
				continue
			}
			strip(c)
			c = next
		}
	}
	var b strings.Builder
	for _, n := range nodes {
		if n.Type == html.ElementNode && n.Data == "img" && !allowed(srcOf(n)) {
			continue
		}
		strip(n)
		if err := html.Render(&b, n); err != nil {
			return out
		}
	}
	return b.String()
}
