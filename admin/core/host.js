// Mounts a screen: the design layer's view if there is one and it works, otherwise the pristine built-in
// view (design-defaults/). A design view that cannot be loaded, throws, hangs or returns something
// unusable is replaced by the built-in view and a console warning; it can never block login or the app.
import { designView, defaultView, warn } from "../lib/design.js";

const TIMEOUT_MS = 20000;

const withTimeout = (p, ms) => new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error(`view did not finish within ${ms / 1000}s`)), ms);
  Promise.resolve(p).then((v) => { clearTimeout(timer); resolve(v); }, (e) => { clearTimeout(timer); reject(e); });
});

// API and core-action failures are about the DATA, not about a broken design: show them, do not fall back.
const isDataError = (e) => Boolean(e) && (typeof e.status === "number" || e.isActionError === true);

/**
 * id: route id. makeRoot(): a fresh container per attempt. makeCtx(): a fresh ctx per attempt.
 * validate(result, root): optional; false means the design view's return value is unusable.
 * Returns {root, result, source}.
 */
export async function mountView(id, makeRoot, makeCtx, { validate } = {}) {
  const design = await designView(id);
  if (design) {
    const root = makeRoot();
    try {
      const result = await withTimeout(design(root, makeCtx()), TIMEOUT_MS);
      if (!validate || validate(result, root)) return { root, result, source: "design" };
      warn(`design view "${id}" returned an unusable result; using the built-in view`);
    } catch (e) {
      if (isDataError(e)) throw e;
      warn(`design view "${id}" failed; using the built-in view`, e);
    }
  }
  const root = makeRoot();
  const def = await defaultView(id);
  const result = await def(root, makeCtx());
  return { root, result, source: "default" };
}
