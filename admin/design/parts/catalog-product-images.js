// The "圖片" section of the product editor. Images are a sub-resource: every operation takes effect at once through its own
// core action and is never part of the product save. After each operation the whole list is re-read from the server.
// The view only hands a File chosen through <input type=file> to the core, which owns limits, HEIC conversion and ordering.

/** Builds the fieldset#product-images for `product` (null = not yet saved). */
export function productImagesSection(ctx, product) {
  const { h, clear } = ctx.ui;
  const box = h("fieldset", { class: "obj product-images", id: "product-images" }, h("legend", {}, ctx.t("productImages.title")));
  const body = h("div", { class: "images-body" });
  box.append(body);

  const media = ctx.logic.productMedia();
  if (!media.enabled) {
    body.append(h("p", { class: "hint", id: "images-disabled" }, ctx.t("productImages.disabled")));
    return box;
  }
  if (!product) {
    body.append(h("p", { class: "hint" }, ctx.t("productImages.saveFirst")));
    return box;
  }

  const editable = ctx.can("product.edit");
  const errorBox = h("p", { class: "error", hidden: true });
  const list = h("div", { class: "image-list" });
  let images = (product.images || []).slice();

  const fail = (e) => {
    errorBox.textContent = (e && e.message) || String(e);
    errorBox.hidden = false;
  };

  async function reload() {
    try {
      images = await ctx.data.listProductImages(product.id);
    } catch (e) {
      fail(e);
    }
    if (ctx.alive()) draw();
  }

  // Every operation: clear the error -> do it -> show a failure -> re-read the whole list (unless the person declined).
  async function act(fn) {
    errorBox.hidden = true;
    try {
      const r = await fn();
      if (r && r.cancelled) return;
    } catch (e) {
      fail(e);
    }
    await reload();
  }

  function draw() {
    clear(list);
    if (images.length === 0) {
      list.append(h("p", { class: "hint" }, ctx.t("productImages.none")));
      return;
    }
    images.forEach((im, i) => {
      const alt = h("input", { type: "text", class: "image-alt", maxlength: "300", Value: im.alt, placeholder: ctx.t("productImages.altPlaceholder") });
      alt.disabled = !editable;
      alt.addEventListener("change", () => act(() => ctx.actions.setProductImageAlt(product.id, im.id, alt.value)));
      list.append(h("div", { class: "image-row", "data-image-id": im.id },
        h("img", { src: im.urls.thumb, alt: im.alt, class: "image-thumb", width: "96" }),
        h("div", { class: "image-meta" }, alt,
          h("span", { class: "hint" }, ctx.t("productImages.size", { w: im.width, h: im.height }))),
        editable ? h("div", { class: "image-actions" },
          h("button", { type: "button", class: "tiny image-up", disabled: i === 0, onclick: () => act(() => ctx.actions.moveProductImage(product.id, im.id, -1)) }, ctx.t("productImages.up")),
          h("button", { type: "button", class: "tiny image-down", disabled: i === images.length - 1, onclick: () => act(() => ctx.actions.moveProductImage(product.id, im.id, 1)) }, ctx.t("productImages.down")),
          h("button", { type: "button", class: "tiny danger image-remove", onclick: () => act(() => ctx.actions.deleteProductImage(product.id, im.id)) }, ctx.t("productImages.remove"))) : ""));
    });
  }

  body.append(errorBox, list);

  if (editable) {
    const input = h("input", {
      type: "file", id: "product-image-input", multiple: true,
      // HEIC is allowed on purpose: the core converts it to JPEG before uploading.
      accept: "image/jpeg,image/png,image/webp,image/heic,image/heif,.heic,.heif",
    });
    const status = h("span", { class: "hint", id: "image-upload-status" });
    input.addEventListener("change", async () => {
      const files = Array.from(input.files || []);
      input.value = "";
      errorBox.hidden = true;
      for (const [n, file] of files.entries()) {
        status.textContent = ctx.t("productImages.uploading", { n: n + 1, total: files.length, name: file.name });
        try {
          await ctx.actions.uploadProductImage(product.id, file);
          images = await ctx.data.listProductImages(product.id);
          if (ctx.alive()) draw();
        } catch (e) {
          if (e && e.kind === "limit") { fail(e); break; }
          fail(new Error(ctx.t("productImages.fileError", { name: file.name, message: (e && e.message) || String(e) })));
        }
      }
      status.textContent = "";
      await reload();
    });
    body.append(h("div", { class: "image-upload" },
      h("label", { class: "file-button" }, ctx.t("productImages.upload"), input), status,
      h("span", { class: "hint" }, ctx.t("productImages.hint", { mib: media.maxMiB, max: media.maxImages }))));
  }

  draw();
  return box;
}
