import { notFound } from "next/navigation";

import type { Bundle } from "@/lib/api";
import { resolveLayout, resolvePageComponent } from "@/themes/registry";

export const dynamic = "force-dynamic";

const API_BASE = process.env.API_BASE_URL ?? "http://localhost:8080";

// Working-copy preview: /preview?token=... renders the draft bundle through
// the same theme components as the live storefront (task 9.3 preview action;
// API side is task 7.4 — authenticated, never cached).
export default async function PreviewPage({
  searchParams,
}: {
  searchParams: Promise<{ token?: string }>;
}) {
  const { token } = await searchParams;
  if (!token) {
    notFound();
  }
  const res = await fetch(
    `${API_BASE}/api/v1/render/preview?token=${encodeURIComponent(token)}`,
    { cache: "no-store" },
  );
  if (!res.ok) {
    notFound();
  }
  const bundle = (await res.json()) as Bundle;

  const Layout = resolveLayout(bundle.shop.theme.layout_key);
  const PageComponent = resolvePageComponent(bundle.page.component_key);
  if (!Layout || !PageComponent) {
    return (
      <main style={{ padding: "2rem", fontFamily: "monospace" }}>
        <h1>Theme component not found</h1>
        <p>{bundle.page.component_key}</p>
      </main>
    );
  }
  return (
    <>
      <div
        style={{
          background: "#facc15",
          color: "#111",
          textAlign: "center",
          padding: "0.4rem",
          fontFamily: "system-ui, sans-serif",
          fontSize: "0.875rem",
        }}
      >
        預覽模式 — 顯示工作副本，尚未發佈
      </div>
      <Layout shop={bundle.shop}>
        <PageComponent page={bundle.page} />
      </Layout>
    </>
  );
}
