// The core's function groups. Each group module default-exports {data, actions, logic, enums, ui, format} (all optional);
// core/context.js merges them into the ctx surface that lib/public-api.json whitelists.
import auth from "./auth.js";
import cms from "./cms.js";
import catalog from "./catalog.js";
import orders from "./orders.js";
import members from "./members.js";
import settings from "./settings.js";
import platform from "./platform.js";
import content from "./content.js";

export const groups = { auth, cms, catalog, orders, members, settings, platform, content };
