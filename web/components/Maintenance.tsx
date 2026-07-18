// Maintenance page shown when the shop is disabled (render API 503) or the
// API is unreachable (spec multi-tenancy/Shop status gating).
export function Maintenance() {
  return (
    <main
      style={{
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        minHeight: "100vh",
        gap: "0.75rem",
        fontFamily: "system-ui, sans-serif",
      }}
    >
      <h1 style={{ fontSize: "2rem" }}>維護中</h1>
      <p>本商店暫時無法提供服務，請稍後再試。</p>
    </main>
  );
}
