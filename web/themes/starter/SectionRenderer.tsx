import type { ReactNode } from "react";

import { Hero } from "./sections/Hero";
import { RichText } from "./sections/RichText";
import { FeatureGrid } from "./sections/FeatureGrid";
import { Cta } from "./sections/Cta";
import type { StarterSection } from "./schema-types";

// renderSection maps a section item to its component by the `type`
// discriminator. The native apps will hold the equivalent SwiftUI/Compose
// registry over the same bundle (design D8).
function renderSection(section: StarterSection, key: number): ReactNode {
  switch (section.type) {
    case "hero":
      return <Hero key={key} {...section} />;
    case "rich_text":
      return <RichText key={key} {...section} />;
    case "feature_grid":
      return <FeatureGrid key={key} {...section} />;
    case "cta":
      return <Cta key={key} {...section} />;
    default:
      // Unknown section types are skipped gracefully — never crash — so the
      // server may evolve ahead of deployed clients (design D8).
      console.warn(
        "[starter] unknown section type skipped:",
        (section as { type?: string })?.type,
      );
      return null;
  }
}

// SectionRenderer walks page.content.sections defensively: the API hydration
// already prunes unknown types, but the renderer must survive any payload.
export function SectionRenderer({ sections }: { sections: unknown }) {
  if (!Array.isArray(sections)) {
    return null;
  }
  return <>{sections.map((s, i) => renderSection(s as StarterSection, i))}</>;
}
