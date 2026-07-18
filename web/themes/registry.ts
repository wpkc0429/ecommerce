import type { ComponentType, ReactNode } from "react";

import type { BundlePage, BundleShop } from "@/lib/api";
import StarterLayout from "./starter/Layout";
import StarterSectionPage from "./starter/pages/SectionPage";

// Theme component registry: layout_key / component_key (stored on themes /
// theme_pages rows) resolve to frontend components here (design D1 — 主題 =
// 前端元件庫).

export type LayoutComponent = ComponentType<{ shop: BundleShop; children: ReactNode }>;
export type PageComponent = ComponentType<{ page: BundlePage }>;

const layouts: Record<string, LayoutComponent> = {
  "starter/main": StarterLayout,
};

const pages: Record<string, PageComponent> = {
  "starter/home": StarterSectionPage,
  "starter/about": StarterSectionPage,
  "starter/landing": StarterSectionPage,
};

export function resolveLayout(layoutKey: string): LayoutComponent | undefined {
  return layouts[layoutKey];
}

export function resolvePageComponent(componentKey: string): PageComponent | undefined {
  return pages[componentKey];
}
