import { cache } from "react";

// Render bundle contract (design D8) — the same SDUI structure native apps
// will consume. Content payloads are typed via the generated schema types.
import type { StarterConfig } from "@/themes/starter/schema-types";

export type BundleTheme = {
  code: string;
  layout_key: string;
};

export type BundleShop = {
  id: number;
  name: string;
  theme: BundleTheme;
  // Hydrated shop-global payload. Typed loosely at the transport layer; theme
  // components narrow it (starter uses StarterConfig).
  content: StarterConfig & Record<string, unknown>;
};

export type PageSEO = {
  seo_title?: string;
  seo_keywords?: string;
  seo_description?: string;
  [k: string]: unknown;
};

export type BundlePage = {
  type_key: string;
  component_key: string;
  title: string;
  content: Record<string, unknown>;
  seo: PageSEO;
};

export type Bundle = {
  shop: BundleShop;
  page: BundlePage;
};

export type BundleResult =
  | { status: 200; bundle: Bundle }
  | { status: number; bundle?: undefined };

const API_BASE = process.env.API_BASE_URL ?? "http://localhost:8080";

// Per-request memoized (generateMetadata + page share one fetch). The render
// API itself is Redis-cached; Next never caches (cache: no-store).
export const getBundle = cache(
  async (host: string, path: string): Promise<BundleResult> => {
    let res: Response;
    try {
      res = await fetch(
        `${API_BASE}/api/v1/render/page?path=${encodeURIComponent(path)}`,
        {
          headers: { "X-Site-Domain": host },
          cache: "no-store",
        },
      );
    } catch {
      // API unreachable → behave like maintenance.
      return { status: 503 };
    }
    if (!res.ok) {
      return { status: res.status };
    }
    return { status: 200, bundle: (await res.json()) as Bundle };
  },
);
