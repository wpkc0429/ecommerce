package httpapi

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/content"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/ratelimit"
)

// ContentSchemaHandler is the admin API of per-shop content schema
// registration (change headless-content-store, first slice, design D3/D4/D5).
//
// Registration is a human action. PUT is guarded by content.manage, which only
// merchant_owner and the platform super_admin hold (seed test pins the sets),
// is rate limited per shop and caller, and leaves an audit trail (the version
// row records created_by_user_id; every call also writes a structured audit
// log line). Automation - CI, an AI edit service, a service account - must
// never hold content.manage: docs/content-schema.md.
type ContentSchemaHandler struct {
	Service *content.SchemaService
	Authz   *AuthzMW
	Limiter *ratelimit.Limiter
	Log     *slog.Logger
}

// Rate limits of design D9: registrations per shop and caller per hour, dry
// runs per minute.
const (
	contentSchemaRegisterLimit  = 20
	contentSchemaRegisterWindow = time.Hour
	contentSchemaDryRunLimit    = 10
	contentSchemaDryRunWindow   = time.Minute
)

// MountShopAdmin: routes under /admin/shops/{shopID}.
func (h *ContentSchemaHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("content.view")).Get("/content/schema", h.get)
	r.With(h.Authz.RequireShopPermission("content.view")).Get("/content/schema/versions", h.versions)
	r.With(h.Authz.RequireShopPermission("content.manage")).Put("/content/schema", h.register)
}

func queryBool(r *http.Request, name string) bool {
	v, err := strconv.ParseBool(r.URL.Query().Get(name))
	return err == nil && v
}

func (h *ContentSchemaHandler) register(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	dry := queryBool(r, "dry_run")
	confirm := queryBool(r, "confirm_removals")

	key := fmt.Sprintf("content_schema:%d:%d", shopID, id.UserID)
	limit, window := contentSchemaRegisterLimit, contentSchemaRegisterWindow
	if dry {
		key = fmt.Sprintf("content_schema_dry:%d:%d", shopID, id.UserID)
		limit, window = contentSchemaDryRunLimit, contentSchemaDryRunWindow
	}
	if res := h.Limiter.Allow(r.Context(), key, limit, window); !res.Allowed {
		httpx.TooManyRequests(w, res.RetryAfter)
		return
	}

	// The body limit is 128 KiB (not the usual 1 MiB); one byte more is read so
	// Validate reports "too large" itself with the same 422 as every other rule.
	body, err := io.ReadAll(io.LimitReader(r.Body, content.MaxBodyBytes+2))
	if err != nil {
		httpx.BadRequest(w, "could not read the request body")
		return
	}

	res, err := h.Service.Register(r.Context(), shopID, body, content.RegisterOptions{
		DryRun: dry, ConfirmRemovals: confirm, ActorUserID: id.UserID,
	})
	if err != nil {
		var rem *content.RemovalsError
		var ve *cms.ValidationError
		switch {
		case errors.As(err, &rem):
			httpx.WriteError(w, http.StatusConflict, "removals_require_confirmation", rem.Error(),
				map[string]any{"report": rem.Report})
		case errors.As(err, &ve):
			httpx.Unprocessable(w, ve.Message, ve.Details)
		case errors.Is(err, content.ErrNotFound):
			httpx.NotFound(w)
		default:
			h.Log.Error("content schema register", "err", err)
			httpx.Internal(w)
		}
		return
	}
	out := map[string]any{
		"version": res.Version,
		"changed": res.Changed,
		"dry_run": res.DryRun,
		"report":  res.Report,
	}
	if res.DryRun {
		out["would_register_version"] = res.WouldRegisterVersion
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func versionJSON(v content.VersionInfo) map[string]any {
	return map[string]any{
		"version":            v.Version,
		"format":             v.Format,
		"sha256":             v.SHA256,
		"source":             v.Source,
		"created_by_user_id": v.CreatedByUserID,
		"created_at":         v.CreatedAt,
		"has_snapshot":       v.HasSnapshot,
	}
}

func (h *ContentSchemaHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	version := 0
	if raw := r.URL.Query().Get("version"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpx.BadRequest(w, "version must be a positive integer")
			return
		}
		version = n
	}
	d, err := h.Service.Get(r.Context(), shopID, version, r.URL.Query().Get("include") == "snapshot")
	if err != nil {
		if errors.Is(err, content.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "content_schema_not_found",
				fmt.Sprintf("no content schema (version) for this shop; register one with PUT /api/v1/admin/shops/%d/content/schema", shopID), nil)
			return
		}
		h.Log.Error("content schema get", "err", err)
		httpx.Internal(w)
		return
	}
	out := versionJSON(d.VersionInfo)
	out["schema"] = d.Schema
	if len(d.Snapshot) > 0 {
		out["snapshot"] = d.Snapshot
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *ContentSchemaHandler) versions(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	list, err := h.Service.Versions(r.Context(), shopID)
	if err != nil {
		if errors.Is(err, content.ErrNotFound) {
			httpx.NotFound(w)
			return
		}
		h.Log.Error("content schema versions", "err", err)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, v := range list {
		out = append(out, versionJSON(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"versions": out})
}
