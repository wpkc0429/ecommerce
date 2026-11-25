// Shared bits of the orders views: status badges and the key/value row.
export function label(ctx, map, v) {
  const key = map[v];
  return key ? ctx.t(key) : `#${v}`;
}

export function badge(ctx, map, v, okValues) {
  const cls = okValues.includes(v) ? "badge ok" : "badge";
  return ctx.ui.h("span", { class: cls }, label(ctx, map, v));
}

export function kv(ctx, k, v) {
  const { h } = ctx.ui;
  return h("div", { class: "kv" }, h("span", { class: "kv-k" }, k), h("span", { class: "kv-v" }, v));
}
