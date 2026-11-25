// Content documents (platform content store): the list at #/documents, the editor at #/documents/<key> and the revision
// history at #/documents/<key>/revisions. One route; this view switches on ctx.route.params. The form, validation, the
// optimistic lock, the confirmations and every request live in the core: the parts only draw.
import contentList from "../parts/content-list.js";
import contentEditor from "../parts/content-editor.js";
import contentRevisions from "../parts/content-revisions.js";

export default async function documents(root, ctx) {
  const key = ctx.route.params[0];
  const sub = ctx.route.params[1];
  if (!key) return contentList(root, ctx);
  if (sub === "revisions") return contentRevisions(root, ctx, key);
  return contentEditor(root, ctx, key);
}
