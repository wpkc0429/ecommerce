// Core group "content" (CORE, locked): content-store documents (list, schema-driven editor, revisions, preview token) and the
// content schema status / dry run / registration. See core/content/actions.js for the rules (permissions first, optimistic
// lock, error mapping, fixed-warning confirmations). The form builder is exposed bound to the core upload action.
import { confirmDialog } from "../../ui/kit.js";
import { contentForm, contentField } from "../../ui/content-form.js";
import { fixedWarning } from "../warnings.js";
import { t } from "../../lib/design.js";
import { mediaConfig, mediaEnabled } from "../../lib/upload.js";
import { KEY_RE } from "../../lib/content.js";
import { EDITOR_KINDS } from "../../lib/content-schema.js";
import { reads, createContentActions } from "../content/actions.js";

/** Core-owned confirmation: title/lead-in/buttons from copy (confirm.<id>.*), the warning is FIXED (lib/meta/content.js warned). */
function ask(id, params, { danger = false, detail = "" } = {}) {
  return confirmDialog({
    title: t(`confirm.${id}.title`, params),
    message: t(`confirm.${id}.text`, params),
    warning: fixedWarning(id, params),
    detail,
    confirmText: t(`confirm.${id}.yes`),
    cancelText: t("common.cancel"),
    danger,
  });
}

const actions = createContentActions({ ask });

const data = {
  contentTypes: reads.types,
  contentDocuments: reads.documents,
  contentDocument: reads.document,
  contentRevisions: reads.revisions,
  contentRevision: reads.revision,
  contentSchemaStatus: reads.schemaStatus,
  dryRunContentSchema: reads.dryRunSchema,
};

// Bound hooks: the upload button exists only while media is enabled; the image prefix comes from the media config.
const bind = (opts = {}) => ({
  ...opts,
  upload: mediaEnabled() && !opts.readOnly ? actions.uploadContentImage : null,
  imagePrefix: mediaConfig().public_base_url || "",
});

export default {
  data,
  actions,
  logic: { contentKeyValid: (key) => typeof key === "string" && KEY_RE.test(key) },
  enums: { contentEditorKinds: EDITOR_KINDS },
  ui: {
    contentForm: (schemaNode, value, opts) => contentForm(schemaNode, value, bind(opts)),
    contentField: (schemaNode, pointer, value, opts) => contentField(schemaNode, pointer, value, bind(opts)),
  },
  format: {},
};
