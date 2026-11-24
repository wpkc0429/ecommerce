// Package richtext sanitizes merchant-authored rich text HTML against a single
// allowlist (change cms-rich-text-sanitization, design D4–D6).
//
// The allowlist lives in allowlist.json and is the ONLY definition: a client
// that renders the stored markup (a merchant storefront) is expected to use a
// copy of the same file for its own defense in depth, so the write-side
// authority and the render side can never disagree about what is allowed.
//
// Sanitization is a rewrite, never a rejection (design D2): disallowed tags
// are unwrapped (their text kept), disallowed attributes and URLs are dropped,
// and dangerous containers (script/style/iframe/...) are removed together
// with their content.
package richtext

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

//go:embed allowlist.json
var allowlistJSON []byte

// Allowlist mirrors allowlist.json.
type Allowlist struct {
	Elements          map[string][]string `json:"elements"`
	AttributePatterns map[string]string   `json:"attribute_patterns"`
	URLSchemes        []string            `json:"url_schemes"`
	AllowRelativeURLs bool                `json:"allow_relative_urls"`
	DropWithContent   []string            `json:"drop_with_content"`
}

var (
	allowlist Allowlist
	policy    *bluemonday.Policy
)

func init() {
	if err := json.Unmarshal(allowlistJSON, &allowlist); err != nil {
		panic(fmt.Sprintf("richtext: invalid allowlist.json: %v", err))
	}
	p, err := buildPolicy(allowlist)
	if err != nil {
		panic(fmt.Sprintf("richtext: %v", err))
	}
	policy = p
}

// Rules returns a copy of the embedded allowlist (tests, diagnostics).
func Rules() Allowlist { return allowlist }

func buildPolicy(a Allowlist) (*bluemonday.Policy, error) {
	p := bluemonday.NewPolicy()
	p.AllowURLSchemes(a.URLSchemes...)
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(a.AllowRelativeURLs)
	p.SkipElementsContent(a.DropWithContent...)

	tags := make([]string, 0, len(a.Elements))
	for tag := range a.Elements {
		tags = append(tags, tag)
	}
	sort.Strings(tags) // deterministic policy construction
	for _, tag := range tags {
		attrs := a.Elements[tag]
		if len(attrs) == 0 {
			p.AllowElements(tag)
			continue
		}
		// Allowed with or without attributes: a <td> whose colspan was
		// invalid must stay a <td>, not vanish.
		p.AllowElements(tag)
		for _, attr := range attrs {
			if pat, ok := a.AttributePatterns[tag+"."+attr]; ok {
				re, err := regexp.Compile(pat)
				if err != nil {
					return nil, fmt.Errorf("attribute pattern %s.%s: %w", tag, attr, err)
				}
				p.AllowAttrs(attr).Matching(re).OnElements(tag)
				continue
			}
			p.AllowAttrs(attr).OnElements(tag)
		}
	}
	return p, nil
}

// Sanitize returns the allowlisted form of an HTML fragment. Every <a> that
// carries target="_blank" additionally gets rel="noopener noreferrer"; any
// author-supplied rel is never accepted (rel is not in the allowlist).
func Sanitize(in string) string {
	if in == "" {
		return ""
	}
	out := policy.Sanitize(in)
	if strings.Contains(out, `target="_blank"`) {
		out = addNoopener(out)
	}
	return out
}

// addNoopener re-serializes the (already sanitized, therefore well-formed)
// fragment, adding rel="noopener noreferrer" to every <a target="_blank">.
// Done as a post-pass rather than with bluemonday's link options because
// those either touch every link (noreferrer everywhere) or only fully
// qualified ones, and the spec rule is exactly "target=_blank ⇒ rel".
func addNoopener(fragment string) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), ctx)
	if err != nil {
		// Unreachable for bluemonday output (always well-formed). Failing
		// closed would drop content; failing open keeps sanitized HTML
		// without the rel — still safe, only window.opener exposure.
		return fragment
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			blank := false
			for _, at := range n.Attr {
				if at.Key == "target" && at.Val == "_blank" {
					blank = true
				}
			}
			if blank {
				kept := n.Attr[:0]
				for _, at := range n.Attr {
					if at.Key != "rel" {
						kept = append(kept, at)
					}
				}
				n.Attr = append(kept, html.Attribute{Key: "rel", Val: "noopener noreferrer"})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	var b strings.Builder
	for _, n := range nodes {
		walk(n)
		if err := html.Render(&b, n); err != nil {
			return fragment
		}
	}
	return b.String()
}
