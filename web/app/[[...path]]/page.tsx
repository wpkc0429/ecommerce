import type { Metadata } from "next";
import { headers } from "next/headers";
import { notFound } from "next/navigation";

import { getBundle, type BundleResult } from "@/lib/api";
import { resolveLayout, resolvePageComponent } from "@/themes/registry";
import { Maintenance } from "@/components/Maintenance";

// Every request depends on Host + the render API — never statically built.
export const dynamic = "force-dynamic";

type Props = { params: Promise<{ path?: string[] }> };

async function load(params: Props["params"]): Promise<BundleResult> {
  const h = await headers();
  const host = h.get("x-site-domain") ?? h.get("host") ?? "";
  const { path = [] } = await params;
  return getBundle(host, "/" + path.join("/"));
}

// SEO (task 8.3): page.seo drives the document metadata.
export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const result = await load(params);
  if (result.status !== 200 || !result.bundle) {
    return {};
  }
  const { shop, page } = result.bundle;
  const seo = page.seo ?? {};
  return {
    title: seo.seo_title || page.title || shop.name,
    description: seo.seo_description || undefined,
    keywords: seo.seo_keywords || undefined,
  };
}

export default async function StorefrontPage({ params }: Props) {
  const result = await load(params);

  if (result.status === 503) {
    // Shop disabled (or API down): the SSR shows the maintenance page
    // (spec multi-tenancy/Shop status gating).
    return <Maintenance />;
  }
  if (result.status !== 200 || !result.bundle) {
    notFound();
  }
  const bundle = result.bundle;

  const Layout = resolveLayout(bundle.shop.theme.layout_key);
  const PageComponent = resolvePageComponent(bundle.page.component_key);
  if (!Layout || !PageComponent) {
    // Explicit error instead of a white screen (design risk: component_key
    // 解析失敗時 SSR 顯式報錯).
    return (
      <main style={{ padding: "2rem", fontFamily: "monospace" }}>
        <h1>Theme component not found</h1>
        <p>
          layout_key: {bundle.shop.theme.layout_key} / component_key:{" "}
          {bundle.page.component_key}
        </p>
      </main>
    );
  }

  return (
    <Layout shop={bundle.shop}>
      <PageComponent page={bundle.page} />
    </Layout>
  );
}
