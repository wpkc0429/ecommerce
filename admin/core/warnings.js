// FIXED WARNINGS for the confirmation of irreversible actions. CORE-OWNED and LOCKED (hash manifest).
//
// Each irreversible action's confirm dialog is composed of:
//   * an editable part from design/copy.js: confirm.<id>.title, confirm.<id>.text (lead-in, may be empty), the button labels
//   * the FIXED warning (lib/meta/*.js `warned`), which the dialog always renders (ui/kit.js confirmDialog({warning}))
// The warning has no copy key, so a design cannot weaken, reword or remove it: lib/design-schema.js validateCopy()
// rejects any attempt to define one and scripts/check-design.mjs fails on it. {name} placeholders are filled from
// the params the core action passes. Adapted from merchant-template v0.3.0 (see docs/DESIGN-LAYER-PROVENANCE.md).
import { WARNINGS, WARNED_IDS } from "../lib/meta/index.js";

export const FIXED_WARNINGS = Object.freeze({ ...WARNINGS });
export const WARNED_CONFIRMS = Object.freeze([...WARNED_IDS]);

export function fixedWarning(id, params = {}) {
  const s = WARNINGS[id];
  if (typeof s !== "string" || !s) throw new Error(`no fixed warning for "${id}"`);
  return s.replace(/\{([A-Za-z][A-Za-z0-9]*)\}/g, (m, name) => (name in params ? String(params[name]) : m));
}
