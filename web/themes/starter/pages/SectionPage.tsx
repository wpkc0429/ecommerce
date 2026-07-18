import type { BundlePage } from "@/lib/api";
import { SectionRenderer } from "../SectionRenderer";

// All starter page types (home / about / landing_page) share the sections
// composition — the schema drives the differences, not the component.
export default function SectionPage({ page }: { page: BundlePage }) {
  return <SectionRenderer sections={page.content?.sections} />;
}
