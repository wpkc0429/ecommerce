import sanitizeHtml from "sanitize-html";
import allowlist from "./richtext-allowlist.json";

// Render-side rich text sanitization (change cms-rich-text-sanitization,
// design D1): defense in depth behind the API, which already sanitized the
// value when it was written. Both sides read the SAME allowlist
// (api/internal/cms/richtext/allowlist.json, copied here by
// `npm run gen:types`), so this pass is normally a no-op — it exists for rows
// written before the API sanitized, or by any path that ever bypasses it.
//
// sanitize-html is used rather than DOMPurify because it needs no DOM: it
// runs as-is in React Server Components / SSR.

type Allowlist = {
  elements: Record<string, string[]>;
  attribute_patterns: Record<string, string>;
  url_schemes: string[];
  allow_relative_urls: boolean;
  drop_with_content: string[];
};

const rules = allowlist as Allowlist;

const patterns: Record<string, Record<string, RegExp>> = {};
for (const [key, pattern] of Object.entries(rules.attribute_patterns)) {
  const [tag, attr] = key.split(".");
  (patterns[tag] ??= {})[attr] = new RegExp(pattern);
}

// Attribute value patterns (img src https-only, numeric colspan, ...) are
// applied in transformTags, which sanitize-html runs before its own
// allowedAttributes / scheme filtering.
const transformTags: sanitizeHtml.IOptions["transformTags"] = {};
for (const tag of Object.keys(rules.elements)) {
  transformTags[tag] = (tagName, attribs) => {
    const tagPatterns = patterns[tag] ?? {};
    const kept: sanitizeHtml.Attributes = {};
    for (const [name, value] of Object.entries(attribs)) {
      const re = tagPatterns[name];
      if (re && !re.test(value)) continue;
      kept[name] = value;
    }
    // Same rule as the API: target="_blank" ⇒ rel="noopener noreferrer"
    // (rel itself is never accepted from the author).
    if (tagName === "a" && kept.target === "_blank") {
      kept.rel = "noopener noreferrer";
    }
    return { tagName, attribs: kept };
  };
}

const options: sanitizeHtml.IOptions = {
  allowedTags: Object.keys(rules.elements),
  allowedAttributes: Object.fromEntries(
    Object.entries(rules.elements).map(([tag, attrs]) => [
      tag,
      tag === "a" ? [...attrs, "rel"] : attrs, // rel is only ever set by transformTags above
    ]),
  ),
  allowedSchemes: rules.url_schemes,
  allowedSchemesByTag: {},
  allowProtocolRelative: false,
  // Containers removed together with their content (script, style, iframe...).
  nonTextTags: rules.drop_with_content,
  disallowedTagsMode: "discard",
  transformTags,
};

export function sanitizeRichText(html: string | null | undefined): string {
  if (!html) return "";
  return sanitizeHtml(html, options);
}
