# Content schema registration

Status: **built** (change `headless-content-store`, first slice). The rest of that change (generic content documents by key, revisions, the public list/preview endpoints, publish hook, template functions) is **not built**; see `openspec/changes/headless-content-store/tasks.md` for what is done and what remains.

A shop's editable content (the pages and the shop-global content stored by the CMS) is validated against **that shop's own content schema**. A new shop has none, so creating a page returns `422 shop has no content schema`. A human registers a schema with one API call. This page is the reference for that call.

## Who may register, and why only a human

Registration is guarded by the permission node **`content.manage`**. Seeded holders:

| Role | content.view | content.edit | content.publish | content.manage |
| --- | --- | --- | --- | --- |
| `super_admin` (platform) | yes | yes | yes | **yes** |
| `merchant_owner` | yes | yes | yes | **yes** |
| `editor` | yes | yes | no | no |
| `staff` | no | no | no | no |

(`page.*` keeps working as an equivalent alias on the existing page endpoints, during the compatibility period; it never grants schema registration.)

**The schema decides the shape of the shop's content, and the file that holds it (`public/content.schema.json` in the merchant's design layer) is editable by an AI agent.** Registration is therefore the one human checkpoint: the owner (or a platform administrator) reads the dry-run report and confirms. Decided 2026-11-25 (Q3):

- Nothing automated may register a schema: **not CI, not the AI edit service, not a deploy hook, not a script run unattended.** Never give a service account, an API token for automation, or any role used by automation the `content.manage` node. Never put a human's admin credentials into CI.
- The platform enforces what it can: only `merchant_owner` and `super_admin` hold the node (a seed test pins this set by equality), an owner cannot hand it to anyone else (only `editor` and `staff` are merchant-assignable roles, and neither holds it; the owner role is appointed by the platform), calls are rate limited per shop and caller (20 registrations per hour, 10 dry runs per minute), and **every call is audited**: the version row stores `created_by_user_id`, and each registration and dry run writes a structured `content audit` log line (ids and counts, never the schema or any content).
- Automating registration would need its own change with a dedicated credential. Until then, treat any pipeline that calls `PUT .../content/schema` as a defect.

## One-call registration, step by step

Starter schema: [`docs/content-schema/starter.content.schema.json`](content-schema/starter.content.schema.json). It matches the merchant template's content fields (`public/content.js`: brand, store info, hero, story, FAQ, product info) and is checked by a unit test, so it always passes the validator.

```bash
API=https://api.example.com        # your API base
SHOP=12                            # the shop id
TOKEN=...                          # an admin access token of a human who holds content.manage (do not paste it into logs)
SCHEMA=docs/content-schema/starter.content.schema.json   # or the merchant's public/content.schema.json

# 1. Dry run: validates, reports what would change, writes NOTHING.
curl -s -X PUT "$API/api/v1/admin/shops/$SHOP/content/schema?dry_run=true" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' --data-binary @"$SCHEMA"

# 2. Read the report. 422 = the schema is not acceptable (details have JSON Pointers). 409 on the real call = confirm needed (below).

# 3. Register for real.
curl -s -X PUT "$API/api/v1/admin/shops/$SHOP/content/schema" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' --data-binary @"$SCHEMA"

# 4. Check.
curl -s "$API/api/v1/admin/shops/$SHOP/content/schema" -H "Authorization: Bearer $TOKEN"        # active version
curl -s "$API/api/v1/admin/shops/$SHOP/content/schema/versions" -H "Authorization: Bearer $TOKEN"
```

Then create pages as before: `POST /api/v1/admin/shops/{shopID}/pages` with `type_key` one of the schema's types (not `global`, which is the shop-global content), edit with `PUT /pages/{id}`, `POST /pages/{id}/publish`, and the public `GET /api/v1/render/page?path=/<slug>` (with the shop's publishable key) serves it, hydrated with the schema defaults.

A new shop's auto-created `home` page holds an empty placeholder. If the registered schema has a `home` type that the placeholder does not satisfy, the dry-run report lists it as `needs_attention` (nothing is lost: it is empty). Save it once with real content (`PUT /pages/{id}`) and it is fixed.

### Responses

`PUT .../content/schema`:

```json
{ "version": 1, "changed": true, "dry_run": false,
  "report": { "documents": [
    { "kind": "page", "key": "home", "status": "needs_attention",
      "removed_paths": [], "invalid": [{"pointer": "/sections", "message": "..."}], "published_invalid": false } ] } }
```

A dry run adds `would_register_version` and never changes `version` (the currently active one, `0` if none). Registering a document byte-for-byte equal to the active one (key order and whitespace do not matter) answers `changed: false` and does not add a version.

### Versioning, removals and `needs_attention`

- Every registration with different content is a new, immutable **version**; the active one is the highest. Registration **never rewrites a document**.
- For each existing page and the global content the report says: `ok`; `needs_attention` (the draft, or the published snapshot, no longer validates); or `orphaned` (the page's `type_key` is gone from the schema).
- `removed_paths` lists values that the new schema would **drop** when content is hydrated (a removed or renamed field that holds a value; empty values do not count). If any exist, or an orphaned page holds content, the real call answers **409 `removals_require_confirmation`** with the report, and writes nothing. Repeat with `?confirm_removals=true` to proceed.
- On confirmed registration, in the same transaction, a **snapshot** of every at-risk document (its draft and, if different, its published content as they were just before) is stored with the new version: `GET .../content/schema?version=N&include=snapshot`. Edited drafts are rebuilt from the new schema's fields, so the snapshot is where an old field's value survives.
- Pages that no longer validate get `needs_attention: true`. **Their published content keeps being served** (hydrated under the new schema). The draft must be fixed before it can be saved with new content or published: a `PUT` of content that does not validate is `422`; saving a valid draft or publishing clears the flag.
- Roll back by registering the older document again (a new version with the old content).

## What a schema looks like (format 2)

```json
{
  "format": 2,
  "types": {
    "home":   { "title": "Home",   "schema": { "type": "object", "additionalProperties": false, "properties": { "...": {} } } },
    "global": { "title": "Global", "schema": { "...": {} } }
  },
  "documents":   { "home": { "type": "home" }, "global": { "type": "global" } },
  "collections": { "campaign-": { "type": "campaign", "max": 20 } }
}
```

- `types.<key>.schema` is a restricted JSON Schema (draft 2020-12). Type keys match `^[a-z0-9][a-z0-9_-]{0,49}$`. The type named **`global`** is the schema of the shop-global content (`GET/PUT .../content`); the other types are page types.
- `documents` and `collections` declare fixed document keys and dynamic key prefixes. They are **validated and stored in this slice, but not enforced or acted on yet** (no content documents exist yet; pages are still keyed by slug). They are what the later slice of the change builds on, so declare them now.
- Every property that is not an object or array needs a **`default`** that satisfies its own constraints, and the whole schema must accept its own hydrated defaults (checked at registration; the error names the field). Arrays default to `[]`.

### Allowed vocabulary (anything else is rejected with a JSON Pointer)

| Area | Allowed |
| --- | --- |
| Types | `object`, `array`, `string`, `integer`, `number`, `boolean` (a single string, no unions, no `null`) |
| Structure | `properties`, `required`, `items`, `additionalProperties` (**must be `false` on every object**), `enum`, `const`, `default`, `title`, `description`, `$defs`, `$ref`, `oneOf` |
| `$ref` | only `#/$defs/<name>` (local); no cycles; expansion may not exceed 5,000 nodes or 16 levels |
| `oneOf` | only as the `items` of an array, as a **block union**: every branch is an object whose `properties.type.const` is a distinct string (the "sections" convention) |
| Strings | `minLength`, **`maxLength` required** (1..2000; `richtext` up to 50,000; `link` and `image` up to 2,048; not required when `const` or `enum` fixes the value), `pattern` (RE2, at most 200 characters), `format` (`date` or `date-time` only) |
| Arrays | `minItems`, **`maxItems` required** (1..200), `items` required |
| Numbers | `minimum`, `maximum` |
| Editor hints | string: `x-editor` = `text` (default), `textarea`, `richtext`, `image`, `link`, `color`, `select` (needs `enum`); array: `x-editor` = `list` (default) or `sections`; `x-editor-order` (integer); `x-help` (plain text, at most 200 characters) |
| Text | `title`, `description`, `x-help` and type titles are plain text: at most 200/500/200/200 characters and no `<` or `>` |
| `$schema` | only `https://json-schema.org/draft/2020-12/schema`, only at the root of a type |

Forbidden: `$id`, `$anchor`, `$dynamicRef`, `$dynamicAnchor`, remote or relative `$ref`, `patternProperties`, `propertyNames`, `unevaluated*`, `dependent*`, `if`/`then`/`else`, `allOf`/`anyOf`/`not`, `contentEncoding`/`contentMediaType`, any other `format`, `uniqueItems`, `multipleOf`, any unlisted `x-*`, duplicate object keys.

### Limits

Body 128 KiB; raw JSON nesting 40 levels (checked before parsing); schema nesting depth 8 (properties/items/oneOf levels); at most 2,000 schema nodes in the document; 50 properties per object; 100 enum values of at most 200 characters; 50 types; 100 documents; 20 collections (`max` 1..100 each); 50 definitions per type; a **2-second** wall-clock budget for compiling and the defaults check. Property names match `^[A-Za-z_][A-Za-z0-9_-]{0,63}$` (not `__proto__`, `constructor`, `prototype`). Errors are reported with a JSON Pointer into the submitted document, at most 50 at a time.

Field semantics the storefront relies on: `link` values must be `https://`, `http://`, `mailto:`, `tel:` or a site-relative path (checked by the later slice's write gate; `image` fields follow the image-domain rule when media is configured; `richtext` is sanitized on write by the existing allowlist). Text a template renders from the multi-line `title` fields should turn newlines into line breaks (the starter uses `textarea` fields where the template's `content.js` has `titleHtml` with `<br>`).

## Notes for integrators

- Read endpoints (`GET .../content/schema`, `.../versions`) need `content.view`.
- Errors: `404 content_schema_not_found` (no schema or no such version; the message names the registration call), `409 removals_require_confirmation`, `422 validation_failed` with pointers, `429 rate_limited`.
- The existing page endpoints accept `content.*` and `page.*` equivalently: `page.view`/`content.view` read, `page.create`/`page.edit`/`content.edit` create and edit, `page.publish`/`page.delete`/`content.publish` publish, unpublish and delete.
- Contract: `api/openapi/openapi.yaml` (`registerContentSchema`, `getContentSchema`, `listContentSchemaVersions`).
- Registering a format 2 schema also creates the documents it declares under `documents` that do not exist yet, as empty drafts filled with the type's defaults, and keeps each document's `declared` and `needs_attention` flags current. The snapshot of content a registration put at risk is stored as `schema_snapshot` revisions of the document (read back with `GET .../content/schema?version=N&include=snapshot`, or per document under `.../content/documents/{key}/revisions`). Documents, the public read API and the consumer guide: [`content-store.md`](content-store.md).
