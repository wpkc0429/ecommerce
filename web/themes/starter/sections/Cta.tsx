import type { CtaSection } from "../schema-types";

export function Cta({ text, button_text, button_href }: CtaSection) {
  return (
    <section
      style={{
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        gap: "calc(var(--spacing-unit) * 1)",
        padding: "calc(var(--spacing-unit) * 3) calc(var(--spacing-unit) * 2)",
        background: "color-mix(in srgb, var(--color-primary) 8%, var(--color-background))",
      }}
    >
      <p style={{ fontSize: "1.25rem", fontWeight: 600 }}>{text}</p>
      {button_text ? (
        <a
          href={button_href || "/"}
          style={{
            padding: "0.7rem 1.5rem",
            background: "var(--color-primary)",
            color: "#fff",
            borderRadius: "var(--radius)",
            textDecoration: "none",
            fontWeight: 600,
          }}
        >
          {button_text}
        </a>
      ) : null}
    </section>
  );
}
