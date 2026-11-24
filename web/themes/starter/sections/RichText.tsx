import { sanitizeRichText } from "@/lib/sanitize";
import type { RichTextSection } from "../schema-types";

// Rich text authored in the admin editor. The API sanitizes it on write
// against the shared allowlist (change cms-rich-text-sanitization); it is
// sanitized again here with the same allowlist before it reaches the DOM.
export function RichText({ html }: RichTextSection) {
  return (
    <section
      style={{
        maxWidth: "48rem",
        margin: "0 auto",
        padding: "calc(var(--spacing-unit) * 2)",
        lineHeight: 1.7,
      }}
      dangerouslySetInnerHTML={{ __html: sanitizeRichText(html) }}
    />
  );
}
