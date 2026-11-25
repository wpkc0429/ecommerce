// Built-in copy (pristine). Aggregates the per-group fragments in design-defaults/copy/. design/copy.js is the editable
// flat copy generated from this by scripts/sync-design.mjs; keys unknown here are ignored by the loader.
import common from "./copy/common.js";
import auth from "./copy/auth.js";
import cms from "./copy/cms.js";
import catalog from "./copy/catalog.js";
import orders from "./copy/orders.js";
import members from "./copy/members.js";
import settings from "./copy/settings.js";
import platform from "./copy/platform.js";
import content from "./copy/content.js";
import publishing from "./copy/publishing.js";

export default { ...common, ...auth, ...cms, ...catalog, ...orders, ...members, ...settings, ...platform, ...content, ...publishing };
