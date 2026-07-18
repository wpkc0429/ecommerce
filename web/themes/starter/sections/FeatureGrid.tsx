import type { FeatureGridSection } from "../schema-types";

export function FeatureGrid({ title, items }: FeatureGridSection) {
  return (
    <section style={{ padding: "calc(var(--spacing-unit) * 3) calc(var(--spacing-unit) * 2)" }}>
      {title ? (
        <h2 style={{ textAlign: "center", marginBottom: "calc(var(--spacing-unit) * 2)" }}>
          {title}
        </h2>
      ) : null}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(14rem, 1fr))",
          gap: "calc(var(--spacing-unit) * 1.5)",
          maxWidth: "64rem",
          margin: "0 auto",
        }}
      >
        {(items ?? []).map((item, i) => (
          <div
            key={i}
            style={{
              border: "1px solid rgba(0,0,0,0.08)",
              borderRadius: "var(--radius)",
              padding: "calc(var(--spacing-unit) * 1.25)",
              display: "flex",
              flexDirection: "column",
              gap: "0.5rem",
            }}
          >
            {item.icon ? (
              // eslint-disable-next-line @next/next/no-img-element
              <img src={item.icon} alt="" style={{ width: "2.5rem", height: "2.5rem" }} />
            ) : null}
            <strong>{item.title}</strong>
            <span style={{ opacity: 0.8 }}>{item.text}</span>
          </div>
        ))}
      </div>
    </section>
  );
}
