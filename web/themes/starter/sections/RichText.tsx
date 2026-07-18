import type { RichTextSection } from "../schema-types";

// Rich text authored by merchant staff in the admin editor. Phase 1 trusts
// back-office authors; HTML sanitization belongs with the Phase 2 editor
// hardening if authorship ever widens.
export function RichText({ html }: RichTextSection) {
  return (
    <section
      style={{
        maxWidth: "48rem",
        margin: "0 auto",
        padding: "calc(var(--spacing-unit) * 2)",
        lineHeight: 1.7,
      }}
      dangerouslySetInnerHTML={{ __html: html ?? "" }}
    />
  );
}
