import type { HeroSection } from "../schema-types";

export function Hero({ title, subtitle, image, cta_text, cta_href }: HeroSection) {
  return (
    <section
      style={{
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        textAlign: "center",
        gap: "calc(var(--spacing-unit) * 1)",
        padding: "calc(var(--spacing-unit) * 4) calc(var(--spacing-unit) * 2)",
        backgroundImage: image ? `url(${image})` : undefined,
        backgroundSize: "cover",
        backgroundPosition: "center",
      }}
    >
      <h1 style={{ fontSize: "2.5rem", lineHeight: 1.2 }}>{title}</h1>
      {subtitle ? <p style={{ fontSize: "1.125rem", opacity: 0.8 }}>{subtitle}</p> : null}
      {cta_text ? (
        <a
          href={cta_href || "/"}
          style={{
            display: "inline-block",
            marginTop: "calc(var(--spacing-unit) * 0.5)",
            padding: "0.75rem 1.75rem",
            background: "var(--color-primary)",
            color: "#fff",
            borderRadius: "var(--radius)",
            textDecoration: "none",
            fontWeight: 600,
          }}
        >
          {cta_text}
        </a>
      ) : null}
    </section>
  );
}
