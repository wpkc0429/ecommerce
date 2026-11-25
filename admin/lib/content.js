// Content-store API client (change headless-content-store): schema read/dry-run/register, documents, revisions, preview token.
// Contract: ecommerce docs/content-store.md, docs/content-schema.md and api/openapi/openapi.yaml. Adapted from merchant-template v0.3.0 lib/content.js.
// All URLs, the If-Match / If-None-Match handling and the error classification live here; views never build paths.
//
// Optimistic lock: a document has `draft_rev` (also the response header `ETag: "<draft_rev>"`).
//   update  -> PUT with `If-Match: "<rev>"`        mismatch: 412 precondition_failed (details.current_rev)
//   create  -> PUT with `If-None-Match: *`         key exists: 412
//   neither -> 428 precondition_required           (a programming error here: we always send one)
// Failures are thrown as ContentError with a `kind` the core turns into a localized ActionError.
//
// Requests go through lib/api.js apiRaw() (extra headers, token refresh, Access reload, post-403 probe).
import { apiRaw, RawJSON, ApiError } from "./api.js";

export const KEY_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;
/** Platform limits the core checks before sending (docs/content-store.md section 1 and content-schema.md). */
export const MAX_CONTENT_BYTES = 128 * 1024;
export const MAX_SCHEMA_BYTES = 128 * 1024;

export class ContentError extends Error {
  /** kind: stale | precondition_required | invalid | confirm_removals | not_found | forbidden | conflict | rate_limited | other */
  constructor(kind, cause, extra = {}) {
    super(cause?.message || kind);
    this.name = "ContentError";
    this.kind = kind;
    this.status = cause?.status ?? 0;
    this.code = cause?.body?.error?.code || "";
    this.cause = cause;
    this.currentRev = extra.currentRev ?? null;
    this.details = extra.details || [];
    this.report = extra.report || null;
  }
}

/** Classifies an ApiError (anything else is rethrown untouched: a network failure is not a content error). */
export function classify(err) {
  if (!(err instanceof ApiError)) return err;
  const raw = err.body?.error?.details;
  const obj = raw && !Array.isArray(raw) && typeof raw === "object" ? raw : {};
  const code = err.body?.error?.code || "";
  if (err.status === 412) return new ContentError("stale", err, { currentRev: Number.isFinite(Number(obj.current_rev)) ? Number(obj.current_rev) : null });
  if (err.status === 428) return new ContentError("precondition_required", err);
  if (err.status === 422) return new ContentError("invalid", err, { details: Array.isArray(raw) ? raw : [] });
  if (err.status === 409 && code === "removals_require_confirmation") return new ContentError("confirm_removals", err, { report: obj.report || null });
  if (err.status === 409) return new ContentError("conflict", err);
  if (err.status === 404) return new ContentError("not_found", err);
  if (err.status === 403) return new ContentError("forbidden", err);
  if (err.status === 429) return new ContentError("rate_limited", err);
  return new ContentError("other", err);
}
const wrap = async (p) => { try { return await p; } catch (e) { throw classify(e); } };

/** JSON request with optional extra headers and an optional pre-serialised body (raw). Returns the parsed body (null when empty). */
async function request(method, path, { body, raw, headers } = {}) {
  return (await apiRaw(method, path, raw !== undefined ? new RawJSON(raw) : body, headers || {})).data;
}

const base = (shopID) => `/admin/shops/${shopID}/content`;
const docPath = (shopID, key) => `${base(shopID)}/documents/${encodeURIComponent(key)}`;
/** The header value for a revision: `"3"` (quoted, as the platform requires). */
export const etagOf = (rev) => `"${Number(rev)}"`;
const validRev = (rev) => Number.isInteger(rev) && rev > 0;

// ── Normalisation (API snake_case -> the shape the screens use) ────────
export function normalizeSummary(d) {
  return {
    key: d.key, type: d.type_key, title: d.title || "", status: d.status, isPublished: d.status === 1,
    needsAttention: Boolean(d.needs_attention), declared: d.declared !== false,
    rev: d.draft_rev, publishedRev: d.published_rev, schemaVersion: d.schema_version, publishedSchemaVersion: d.published_schema_version,
    updatedAt: d.updated_at, publishedAt: d.published_at || null, scheduledPublishAt: d.scheduled_publish_at || null,
  };
}
export function normalizeDocument(d) {
  return {
    ...normalizeSummary(d),
    typeTitle: d.type_title || "",
    draft: d.draft || {}, published: d.published ?? null, meta: d.meta || {},
    schemaNode: d.schema_node ?? null,
    restoredValid: d.restored_valid,
  };
}

// ── Schema ─────────────────────────────────────────────────────────────
/** The active (or given) schema version: {version, format, sha256, source, createdAt, schema}; null when none is registered. */
export async function getSchema(shopID, version) {
  try {
    const q = version ? `?version=${Number(version)}` : "";
    const d = await request("GET", `${base(shopID)}/schema${q}`);
    return { version: d.version, format: d.format, sha256: d.sha256, source: d.source, createdAt: d.created_at, schema: d.schema };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw classify(e);
  }
}
export async function listSchemaVersions(shopID) {
  try {
    return ((await request("GET", `${base(shopID)}/schema/versions`)).versions || []).map((v) => ({
      version: v.version, format: v.format, sha256: v.sha256, source: v.source, createdAt: v.created_at, hasSnapshot: Boolean(v.has_snapshot),
    }));
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return [];
    throw classify(e);
  }
}
/**
 * PUT the schema document (rawText = the JSON text the human provided, sent byte for byte so the platform's own
 * duplicate-key check sees what the human wrote). dryRun=true writes NOTHING (report only); without dryRun this REGISTERS
 * the schema, which only core/content/actions.js (behind the human confirmation dialog) does.
 */
export function putSchema(shopID, rawText, { dryRun = false, confirmRemovals = false } = {}) {
  const p = new URLSearchParams();
  if (dryRun) p.set("dry_run", "true");
  if (confirmRemovals && !dryRun) p.set("confirm_removals", "true");
  const q = p.toString();
  return wrap(request("PUT", `${base(shopID)}/schema${q ? `?${q}` : ""}`, { raw: rawText }));
}

// ── Documents ──────────────────────────────────────────────────────────
export async function listDocuments(shopID) {
  try { return ((await request("GET", `${base(shopID)}/documents`)).documents || []).map(normalizeSummary); } catch (e) { throw classify(e); }
}
export async function getDocument(shopID, key) {
  try { return normalizeDocument(await request("GET", docPath(shopID, key))); } catch (e) { throw classify(e); }
}
/**
 * Saves a draft. `rev` is the draft_rev the form was loaded with (If-Match); `create: true` sends If-None-Match: * instead.
 * Never sends without a precondition. Returns the document AS THE SERVER STORED IT (rich text is rewritten).
 * body: {content, meta?, title?, typeKey?}.
 */
export async function saveDocument(shopID, key, body, { rev, create = false } = {}) {
  if (!create && !validRev(rev)) throw new Error("saveDocument: a draft revision is required to update (If-Match)");
  const headers = create ? { "If-None-Match": "*" } : { "If-Match": etagOf(rev) };
  const payload = { content: body.content };
  if (body.meta !== undefined) payload.meta = body.meta;
  if (body.title !== undefined) payload.title = body.title;
  if (create && body.typeKey) payload.type_key = body.typeKey;
  try { return normalizeDocument(await request("PUT", docPath(shopID, key), { body: payload, headers })); } catch (e) { throw classify(e); }
}
export async function publishDocument(shopID, key, rev) {
  const headers = validRev(rev) ? { "If-Match": etagOf(rev) } : undefined;
  try { return normalizeSummary(await request("POST", `${docPath(shopID, key)}/publish`, { body: {}, headers })); } catch (e) { throw classify(e); }
}
export async function unpublishDocument(shopID, key) {
  try { return normalizeSummary(await request("POST", `${docPath(shopID, key)}/unpublish`, { body: {} })); } catch (e) { throw classify(e); }
}
export async function deleteDocument(shopID, key) {
  try { await request("DELETE", docPath(shopID, key)); return { ok: true }; } catch (e) { throw classify(e); }
}

// ── Revisions ──────────────────────────────────────────────────────────
const normRevInfo = (r) => ({ rev: r.rev_no, kind: r.kind, title: r.title || "", schemaVersion: r.schema_version, actorUserId: r.actor_user_id ?? null, createdAt: r.created_at });
export async function listRevisions(shopID, key) {
  try { return ((await request("GET", `${docPath(shopID, key)}/revisions`)).revisions || []).map(normRevInfo); } catch (e) { throw classify(e); }
}
export async function getRevision(shopID, key, rev) {
  try {
    const r = await request("GET", `${docPath(shopID, key)}/revisions/${Number(rev)}`);
    return { ...normRevInfo(r), content: r.content || {}, publishedContent: r.published_content ?? null, meta: r.meta || {} };
  } catch (e) { throw classify(e); }
}
/** Puts a revision back into the DRAFT (never publishes). `ifRev` = the draft_rev the screen showed (If-Match, optional). */
export async function restoreRevision(shopID, key, rev, ifRev) {
  const headers = validRev(ifRev) ? { "If-Match": etagOf(ifRev) } : undefined;
  try { return normalizeDocument(await request("POST", `${docPath(shopID, key)}/revisions/${Number(rev)}/restore`, { body: {}, headers })); } catch (e) { throw classify(e); }
}

// ── Preview ────────────────────────────────────────────────────────────
export async function createPreviewToken(shopID, key, scope) {
  try {
    const r = await request("POST", `${docPath(shopID, key)}/preview-token`, { body: { scope } });
    return { token: r.preview_token, expiresIn: r.expires_in, scope: r.scope, key: r.key };
  } catch (e) { throw classify(e); }
}
