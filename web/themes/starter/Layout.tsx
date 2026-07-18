import type { CSSProperties, ReactNode } from "react";

import type { BundleShop } from "@/lib/api";

// Starter layout: consumes design tokens as CSS variables (design D6 —
// config_schema.tokens), header/footer/logo from the hydrated shop.content.
export default function StarterLayout({
  shop,
  children,
}: {
  shop: BundleShop;
  children: ReactNode;
}) {
  const { tokens = {}, header = {}, footer = {} } = shop.content;

  const style = {
    "--color-primary": tokens.color_primary,
    "--color-background": tokens.color_background,
    "--color-text": tokens.color_text,
    "--font-family": tokens.font_family,
    "--spacing-unit": tokens.spacing_unit,
    "--radius": tokens.radius,
    fontFamily: "var(--font-family)",
    background: "var(--color-background)",
    color: "var(--color-text)",
    minHeight: "100vh",
    display: "flex",
    flexDirection: "column",
  } as CSSProperties;

  return (
    <div style={style} data-theme="starter">
      <header
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          padding: "calc(var(--spacing-unit) * 1) calc(var(--spacing-unit) * 2)",
          borderBottom: "1px solid rgba(0,0,0,0.08)",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "0.75rem" }}>
          {header.logo_url ? (
            // eslint-disable-next-line @next/next/no-img-element
            <img
              src={header.logo_url}
              alt={header.site_title ?? shop.name}
              style={{ height: "2.25rem", width: "auto" }}
            />
          ) : null}
          <strong style={{ fontSize: "1.25rem" }}>
            {header.site_title || shop.name}
          </strong>
        </div>
        <nav style={{ display: "flex", gap: "calc(var(--spacing-unit) * 1)" }}>
          {(header.nav ?? []).map((item, i) => (
            <a key={i} href={item.href || "/"} style={{ textDecoration: "none" }}>
              {item.label}
            </a>
          ))}
        </nav>
      </header>

      <main style={{ flex: 1 }}>{children}</main>

      <footer
        style={{
          padding: "calc(var(--spacing-unit) * 1.5) calc(var(--spacing-unit) * 2)",
          borderTop: "1px solid rgba(0,0,0,0.08)",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: "1rem",
          flexWrap: "wrap",
        }}
      >
        <span>{footer.text}</span>
        <nav style={{ display: "flex", gap: "1rem" }}>
          {(footer.links ?? []).map((item, i) => (
            <a key={i} href={item.href || "/"}>
              {item.label}
            </a>
          ))}
        </nav>
      </footer>
    </div>
  );
}
