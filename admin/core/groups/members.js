// Core group "members" (CORE, locked): member tiers, customer roster, member points, reports (+ shop timezone, admin branding).
// data = loaders, actions = permission-checked operations; see core/groups/index.js.
import * as tiers from "../members/tiers.js";
import * as roster from "../members/roster.js";
import * as points from "../members/points.js";
import * as reports from "../members/reports.js";

const parts = [tiers, roster, points, reports];
const merge = (key) => Object.assign({}, ...parts.map((p) => p[key] || {}));

export default { data: merge("data"), actions: merge("actions"), logic: merge("logic"), enums: {}, ui: {}, format: merge("format") };
