// Content documents: the core's loaders and actions for the platform content store. Each function checks its
// permission FIRST (before any request), binds the current shop, validates with the locked schema logic
// (lib/content-schema.js) and maps API failures to ActionErrors:
//   412 -> ActionError kind "stale" (someone else saved first: reload), 428 -> kind "internal" (a programming error, logged,
//   never shown as a user message), 422 -> kind "invalid" with e.fields {JSON Pointer -> message}.
//
// REGISTERING A SCHEMA IS A HUMAN ACTION (docs/content-schema.md). registerContentSchema(text) takes the JSON text a human
// typed or loaded, ALWAYS runs a dry run first and ALWAYS asks through a confirmation dialog that shows the report and the
// FIXED warning; nothing the design layer passes can skip it. The dry run (dryRunContentSchema) only ever sends `?dry_run=true`.
//
// The confirmation function is injected (core/groups/content.js passes confirmDialog) so scripts/test-content.mjs can prove
// that no registering request goes out unless the dialog resolved true.
import { session } from "../../lib/session.js";
import * as content from "../../lib/content.js";
import { validate, hasRemovals, reportLines, hydrate, mapServerDetails } from "../../lib/content-schema.js";
import { mediaConfig, mediaEnabled, uploadContentImage as libUploadContentImage } from "../../lib/upload.js";
import { t } from "../../lib/design.js";
import { need } from "../guard.js";
import { ActionError, invalid, blocked, denied } from "../errors.js";

const S = () => session.shopID;
const CANCELLED = Object.freeze({ cancelled: true });
const bytes = (s) => new TextEncoder().encode(s).length;
const keyOK = (key) => typeof key === "string" && content.KEY_RE.test(key);
const requireKey = (key) => { if (!keyOK(key)) throw new ActionError("state", t("content.err.notFound"), { reason: "gone", status: 404 }); };

/** ContentError (lib/content.js) -> the ActionError the screens understand. Anything else is returned untouched. */
export function toActionError(e, schemaNode) {
  if (!(e instanceof content.ContentError)) return e;
  switch (e.kind) {
    case "stale": {
      const a = new ActionError("stale", t("cf.err.stale"), { reason: "stale" });
      a.currentRev = e.currentRev;
      return a;
    }
    case "precondition_required":
      console.error("[content] 428 precondition_required: a save went out without If-Match / If-None-Match (programming error)");
      return new ActionError("internal", t("cf.err.internal"), { reason: "precondition_required" });
    case "invalid": {
      const fields = mapServerDetails(e.details, schemaNode);
      return invalid(Object.keys(fields).length ? fields : { "": t("content.err.invalid") }, t("content.err.invalid"));
    }
    case "forbidden": return denied();
    case "not_found": return new ActionError("state", t("content.err.notFound"), { reason: "gone", status: 404 });
    case "conflict": return new ActionError("state", e.code === "removals_require_confirmation" ? t("cf.err.removals") : t("cf.err.conflict"), { reason: "conflict" });
    case "rate_limited": return new ActionError("limit", t("content.err.rateLimited"), { reason: "rate_limited", status: 429 });
    default: return e.cause || e;
  }
}
const guarded = async (fn, schemaNode) => { try { return await fn(); } catch (e) { throw toActionError(e, schemaNode); } };

/** The JSON Schema node of a document type in a registered schema document (format 2 {title, schema}, format 1 the node itself); null when unknown. */
export function typeNode(schemaDoc, typeKey) {
  const doc = schemaDoc?.schema;
  if (!doc) return null;
  let n = typeKey === "global" && doc.global ? doc.global : doc.types && doc.types[typeKey];
  if (n && n.schema && typeof n.schema === "object" && !n.type) n = n.schema;
  return n && typeof n === "object" ? n : null;
}

/** [{key, title}] of the document types in a registered schema document. */
export function typeList(schemaDoc) {
  const types = schemaDoc?.schema?.types || {};
  const order = Array.isArray(schemaDoc?.schema?.type_order) ? schemaDoc.schema.type_order : [];
  const keys = [...new Set([...order.filter((k) => k in types), ...Object.keys(types)])];
  const out = keys.map((k) => ({ key: k, title: (types[k] && typeof types[k].title === "string" && types[k].title) || k }));
  if (schemaDoc?.schema?.global && !keys.includes("global")) out.push({ key: "global", title: "global" });
  return out;
}

const summarizeRegister = (r) => ({
  version: r.version, wouldRegisterVersion: r.would_register_version ?? 0, changed: Boolean(r.changed), dryRun: Boolean(r.dry_run),
  report: r.report || { documents: [] }, lines: reportLines(r.report), hasRemovals: hasRemovals(r.report),
});

/** Parses and size-checks the schema text a human provided. Returns the text unchanged (it is sent byte for byte). */
function checkSchemaText(text) {
  const s = typeof text === "string" ? text : "";
  if (!s.trim()) throw invalid({ "": t("content.schema.err.empty") }, t("content.schema.err.empty"));
  if (bytes(s) > content.MAX_SCHEMA_BYTES) throw new ActionError("limit", t("content.schema.err.tooLarge"), { reason: "tooLarge" });
  try { JSON.parse(s); } catch { throw invalid({ "": t("content.schema.err.notJson") }, t("content.schema.err.notJson")); }
  return s;
}

// ── loaders (core/groups/content.js exposes them as ctx.data.*) ───────
export const reads = {
  /** Document types and creation hints of the registered schema ({registered:false} when none). content.view. */
  async types() {
    need("content.view");
    const doc = await guarded(() => content.getSchema(S()));
    if (!doc) return { registered: false, version: 0, types: [], collections: [] };
    const cols = doc.schema?.collections && typeof doc.schema.collections === "object" ? doc.schema.collections : {};
    return {
      registered: true, version: doc.version, format: doc.format, types: typeList(doc),
      collections: Object.entries(cols).map(([prefix, c]) => ({ prefix, type: c?.type || "", max: c?.max ?? 0 })),
    };
  },
  async documents() { need("content.view"); return guarded(() => content.listDocuments(S())); },
  async document(key) {
    need("content.view");
    requireKey(key);
    return guarded(async () => {
      const d = await content.getDocument(S(), key);
      return { ...d, draft: d.schemaNode ? hydrate(d.schemaNode, d.draft) : d.draft };
    });
  },
  async revisions(key) { need("content.view"); requireKey(key); return guarded(() => content.listRevisions(S(), key)); },
  async revision(key, rev) { need("content.view"); requireKey(key); return guarded(() => content.getRevision(S(), key, Number(rev))); },
  /** {active: {version, format, sha256, source, createdAt}|null, versions: [...]}. content.manage (the schema screen). */
  async schemaStatus() {
    need("content.manage");
    const [active, versions] = await Promise.all([guarded(() => content.getSchema(S())), guarded(() => content.listSchemaVersions(S()))]);
    return {
      active: active ? { version: active.version, format: active.format, sha256: active.sha256, source: active.source, createdAt: active.createdAt } : null,
      versions: versions.slice().sort((a, b) => b.version - a.version),
    };
  },
  /** Asks the platform what registering `text` WOULD do. READ-ONLY: only ever sends ?dry_run=true. content.manage. */
  async dryRunSchema(text) {
    need("content.manage");
    const raw = checkSchemaText(text);
    return summarizeRegister(await guarded(() => content.putSchema(S(), raw, { dryRun: true })));
  },
};

// ── actions ────────────────────────────────────────────────────────────
/** deps: { ask(id, params, {danger, detail}) -> Promise<boolean> }. */
export function createContentActions({ ask }) {
  /**
   * values = the whole content object the form read (form.read()). opts = {rev} (the draft_rev the form was loaded with: If-Match).
   * Returns {ok, doc}: the document AS THE SERVER STORED IT; rebuild the form from doc.draft (rich text is rewritten by the server).
   */
  async function saveContentDocument(key, values, opts = {}) {
    need("content.edit");
    requireKey(key);
    const rev = Number(opts.rev);
    if (!(Number.isInteger(rev) && rev > 0)) throw new ActionError("internal", t("cf.err.internal"), { reason: "no_rev" });
    if (!values || typeof values !== "object" || Array.isArray(values)) throw invalid({ "": t("content.err.invalid") });
    if (bytes(JSON.stringify(values)) > content.MAX_CONTENT_BYTES) throw new ActionError("limit", t("content.err.tooLarge"), { reason: "tooLarge" });
    // The schema node comes from the platform (never from the screen), so a screen cannot loosen the validation.
    const cur = await guarded(() => content.getDocument(S(), key));
    const node = cur.schemaNode;
    if (!node) throw blocked("noType", t("cf.err.noType"));
    const errors = validate(node, values, { root: node, imagePrefix: mediaConfig().public_base_url || "" });
    if (Object.keys(errors).length) throw invalid(errors);
    const doc = await guarded(() => content.saveDocument(S(), key, { content: values }, { rev }), node);
    // The save response has no schema_node (only GET does): carry the node we validated with, so the screen can rebuild the form.
    const n = doc.schemaNode || node;
    return { ok: true, doc: { ...doc, schemaNode: n, draft: hydrate(n, doc.draft) } };
  }

  /** Creates a document (If-None-Match: *) of type opts.typeKey filled with the schema defaults. */
  async function createContentDocument({ key, typeKey, title } = {}) {
    need("content.edit");
    if (!keyOK(key)) throw invalid({ "/key": t("content.new.err.key") }, t("content.new.err.key"));
    const doc = await guarded(() => content.getSchema(S()));
    const node = typeNode(doc, String(typeKey || ""));
    if (!node) throw invalid({ "/type": t("cf.err.noType") }, t("cf.err.noType"));
    const values = hydrate(node, {});
    const errors = validate(node, values, { root: node });
    if (Object.keys(errors).length) throw invalid(errors);
    try {
      const saved = await content.saveDocument(S(), key, { content: values, typeKey: String(typeKey), ...(title ? { title: String(title).slice(0, 150) } : {}) }, { create: true });
      return { ok: true, key: saved.key || key };
    } catch (e) {
      // 412 on create (If-None-Match: *) means the key already exists.
      if (e instanceof content.ContentError && e.kind === "stale") throw new ActionError("state", t("content.new.err.exists"), { reason: "exists" });
      throw toActionError(e, node);
    }
  }

  /** opts.rev = the draft_rev the person reviewed (If-Match). A document that needs attention cannot be published. */
  async function publishContentDocument(key, opts = {}) {
    need("content.publish");
    requireKey(key);
    const cur = await guarded(() => content.getDocument(S(), key));
    if (cur.needsAttention) throw blocked("needsAttention", t("cf.err.needsAttention"));
    if (!(await ask("publishContent", { key, title: cur.title || key }, { danger: false }))) return CANCELLED;
    const rev = Number.isInteger(Number(opts.rev)) && Number(opts.rev) > 0 ? Number(opts.rev) : cur.rev;
    const doc = await guarded(() => content.publishDocument(S(), key, rev), cur.schemaNode);
    return { ok: true, doc };
  }

  async function unpublishContentDocument(key) {
    need("content.publish");
    requireKey(key);
    const cur = await guarded(() => content.getDocument(S(), key));
    if (!(await ask("unpublishContent", { key, title: cur.title || key }, { danger: true }))) return CANCELLED;
    return { ok: true, doc: await guarded(() => content.unpublishDocument(S(), key)) };
  }

  async function deleteContentDocument(key) {
    need("content.publish");
    requireKey(key);
    const cur = await guarded(() => content.getDocument(S(), key));
    if (!(await ask("deleteContent", { key, title: cur.title || key }, { danger: true }))) return CANCELLED;
    await guarded(() => content.deleteDocument(S(), key));
    return { ok: true };
  }

  /** Puts a revision back into the DRAFT. It never publishes: the result says so (restoredToDraft). */
  async function restoreContentRevision(key, rev, opts = {}) {
    need("content.edit");
    requireKey(key);
    const n = Number(rev);
    if (!Number.isInteger(n) || n < 1) throw invalid({ "": t("content.err.invalid") });
    if (!(await ask("restoreContent", { key, rev: n }, { danger: false }))) return CANCELLED;
    const doc = await guarded(() => content.restoreRevision(S(), key, n, Number(opts.rev) || undefined));
    // The restore response has no schema_node either: re-read the document (also gives the draft_rev) for the screen.
    const fresh = await guarded(() => content.getDocument(S(), key));
    return { ok: true, restoredToDraft: true, restoredValid: doc.restoredValid !== false, doc: { ...fresh, draft: fresh.schemaNode ? hydrate(fresh.schemaNode, fresh.draft) : fresh.draft } };
  }

  /** file: a File picked in the form. Returns {url (the large variant: the field value)}. Media limits and HEIC conversion live in lib/upload.js. */
  async function uploadContentImage(file) {
    need("content.edit");
    if (!mediaEnabled()) throw new ActionError("state", t("content.err.mediaOff"), { reason: "mediaOff", status: 503 });
    return { url: await libUploadContentImage(file) };
  }

  /**
   * Issues a short-lived preview token for one document (scope "document") or all drafts (scope "shop"). The admin does not know
   * the shop's site URL, so the token is returned for display with its expiry; it is never stored.
   */
  async function createContentPreviewToken(key, opts = {}) {
    need("content.view");
    requireKey(key);
    const tok = await guarded(() => content.createPreviewToken(S(), key, opts.scope === "shop" ? "shop" : "document"));
    const secs = Number(tok.expiresIn) || 0;
    return { token: tok.token, scope: tok.scope, key: tok.key, expiresIn: secs, expiresAt: new Date(Date.now() + secs * 1000).toISOString() };
  }

  /** Reads a human-picked schema file (File object from an <input type=file>) and returns its text for the textarea. */
  async function readContentSchemaFile(file) {
    need("content.manage");
    if (!file || typeof file.text !== "function") throw invalid({ "": t("content.schema.err.file") }, t("content.schema.err.file"));
    if (Number(file.size) > content.MAX_SCHEMA_BYTES) throw new ActionError("limit", t("content.schema.err.tooLarge"), { reason: "tooLarge" });
    let text;
    try { text = await file.text(); } catch { throw invalid({ "": t("content.schema.err.file") }, t("content.schema.err.file")); }
    if (bytes(text) > content.MAX_SCHEMA_BYTES) throw new ActionError("limit", t("content.schema.err.tooLarge"), { reason: "tooLarge" });
    return text;
  }

  /**
   * REGISTERS the schema JSON text a human provided as the shop's content schema. Always dry-runs first, then shows the report
   * in a confirmation dialog with the core's fixed warning; only after the human confirms is the real PUT sent. When the
   * platform answers 409 removals_require_confirmation, a SECOND dialog shows that report and only then is confirm_removals sent.
   */
  async function registerContentSchema(text) {
    need("content.manage");
    const raw = checkSchemaText(text);
    const dry = summarizeRegister(await guarded(() => content.putSchema(S(), raw, { dryRun: true })));
    if (!dry.changed) return { ok: true, unchanged: true, version: dry.version, report: dry.report, lines: dry.lines };
    const detail = dry.lines.length ? dry.lines.join("\n") : t("cf.report.none");
    if (!(await ask("registerContentSchema", { version: dry.wouldRegisterVersion }, { danger: dry.hasRemovals, detail }))) return CANCELLED;
    let res;
    try { res = await content.putSchema(S(), raw, {}); } catch (e) {
      if (!(e instanceof content.ContentError) || e.kind !== "confirm_removals") throw toActionError(e);
      const again = reportLines(e.report || dry.report);
      if (!(await ask("registerContentSchemaRemovals", { version: dry.wouldRegisterVersion }, { danger: true, detail: again.join("\n") || detail }))) return CANCELLED;
      res = await guarded(() => content.putSchema(S(), raw, { confirmRemovals: true }));
    }
    const sum = summarizeRegister(res);
    return { ok: true, version: sum.version, changed: sum.changed, report: sum.report, lines: sum.lines };
  }

  return {
    saveContentDocument, createContentDocument, publishContentDocument, unpublishContentDocument, deleteContentDocument,
    restoreContentRevision, uploadContentImage, createContentPreviewToken, readContentSchemaFile, registerContentSchema,
  };
}
