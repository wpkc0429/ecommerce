package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// DesignServiceHandler is the MACHINE side of the design history (change
// design-history D8/D15), mounted at /api/v1/design-service/shops/{shopID}.
//
// There is no approve, reject, request-changes, cancel or revert route here and
// there never will be: the service writes the record and waits for a person.
// Its only read of a request omits the prompt body.
type DesignServiceHandler struct {
	Service *design.Service
	Log     *slog.Logger
}

// MountService mounts the machine routes under /design-service/shops/{shopID}.
// Authentication and rate limiting are applied by the caller (router.go).
func (h *DesignServiceHandler) MountService(r chi.Router) {
	r.Use(designNoSniff)
	r.Post("/requests", h.create)
	r.Get("/requests", h.queue)
	r.Get("/requests/{requestID}", h.get)
	r.Post("/requests/{requestID}/claim", h.claim)
	r.Put("/requests/{requestID}/head", h.head)
	r.Post("/requests/{requestID}/guard-runs", h.guard)
	r.Put("/requests/{requestID}/preview", h.preview)
	r.Put("/requests/{requestID}/screenshots/{phase}/{role}/{viewport}/{viewKey}", h.uploadScreenshot)
	r.Post("/requests/{requestID}/submit", h.submit)
	r.Post("/requests/{requestID}/published", h.published)
	r.Post("/requests/{requestID}/failed", h.failed)
	// Version previews (design D25): pull model; the service can list, claim and
	// report ready or failed, never create one.
	r.Get("/version-previews", h.vpList)
	r.Post("/version-previews/{previewID}/claim", h.vpClaim)
	r.Put("/version-previews/{previewID}/ready", h.vpReady)
	r.Post("/version-previews/{previewID}/failed", h.vpFailed)
	r.Put("/sites/{site}/state", h.putState)
	r.Post("/untracked-changes", h.untracked)
}

// serviceRequestJSON is what the service may read back: state and ids, not the
// prompt text (history is data, never an instruction for an agent).
func serviceRequestJSON(r *ent.DesignRequest) map[string]any {
	m := requestCommon(r)
	m["preview_origin"] = r.PreviewOrigin
	m["claim_expires_at"] = ts(r.ClaimExpiresAt)
	m["change_note"] = r.ChangeNote
	m["preview_read_only"] = true
	return m
}

func (h *DesignServiceHandler) cred(r *http.Request) int {
	c, _ := designCredentialFrom(r.Context())
	return c.ID
}

type createBody struct {
	RequestID    *uuid.UUID `json:"request_id"`
	Site         string     `json:"site"`
	RequestedBy  int        `json:"requested_by_user_id"`
	Prompt       string     `json:"prompt"`
	BaseSHA      string     `json:"base_sha"`
	Branch       string     `json:"branch"`
	CoreVersion  string     `json:"core_version"`
	FloorVersion string     `json:"design_floor_version"`
}

func (h *DesignServiceHandler) create(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var b createBody
	if !decodeJSON(w, r, &b) {
		return
	}
	row, created, err := h.Service.CreateRequest(r.Context(), design.CreateInput{
		ShopID: shopID, CredentialID: h.cred(r), IdempotencyKey: r.Header.Get("Idempotency-Key"),
		Site: b.Site, RequestedBy: b.RequestedBy, Prompt: b.Prompt, BaseSHA: b.BaseSHA, RequestID: b.RequestID,
		Branch: b.Branch, CoreVersion: b.CoreVersion, FloorVersion: b.FloorVersion,
	})
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("ETag", etagOf(row.Rev))
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
		w.Header().Set("Idempotency-Replayed", "true")
	}
	httpx.WriteJSON(w, status, serviceRequestJSON(row))
}

func (h *DesignServiceHandler) queue(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	q := r.URL.Query()
	rows, err := h.Service.ListQueue(r.Context(), shopID, q.Get("status"), q.Get("claimed") == "false", atoiOr(q.Get("limit"), 50))
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, serviceRequestJSON(row))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (h *DesignServiceHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	row, err := h.Service.Get(r.Context(), shopID, id)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("ETag", etagOf(row.Rev))
	httpx.WriteJSON(w, http.StatusOK, serviceRequestJSON(row))
}

// do runs a request-scoped mutation and writes the resulting request.
func (h *DesignServiceHandler) do(w http.ResponseWriter, r *http.Request, fn func(shopID int, id uuid.UUID) (*ent.DesignRequest, error)) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	row, err := fn(shopID, id)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("ETag", etagOf(row.Rev))
	httpx.WriteJSON(w, http.StatusOK, serviceRequestJSON(row))
}

func (h *DesignServiceHandler) claim(w http.ResponseWriter, r *http.Request) {
	h.do(w, r, func(shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
		return h.Service.Claim(r.Context(), shopID, id, h.cred(r))
	})
}

type headBody struct {
	HeadSHA     string             `json:"head_sha"`
	DiffSummary design.DiffSummary `json:"diff_summary"`
	Reset       bool               `json:"reset"`
}

func (h *DesignServiceHandler) head(w http.ResponseWriter, r *http.Request) {
	var b headBody
	if !decodeJSON(w, r, &b) {
		return
	}
	h.do(w, r, func(shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
		return h.Service.UpdateHead(r.Context(), shopID, id, h.cred(r), b.HeadSHA, b.DiffSummary, b.Reset)
	})
}

type guardBody struct {
	Phase          string   `json:"phase"`
	CheckedSHA     string   `json:"checked_sha"`
	Passed         bool     `json:"passed"`
	ExitCode       int      `json:"exit_code"`
	RuleIDs        []string `json:"rule_ids"`
	ViolationCount int      `json:"violation_count"`
	GuardVersion   string   `json:"guard_version"`
}

func (h *DesignServiceHandler) guard(w http.ResponseWriter, r *http.Request) {
	var b guardBody
	if !decodeJSON(w, r, &b) {
		return
	}
	h.do(w, r, func(shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
		return h.Service.RecordGuard(r.Context(), shopID, id, h.cred(r), design.GuardInput{
			Phase: b.Phase, CheckedSHA: b.CheckedSHA, Passed: b.Passed, ExitCode: b.ExitCode,
			RuleIDs: b.RuleIDs, ViolationCount: b.ViolationCount, GuardVersion: b.GuardVersion,
		})
	})
}

func (h *DesignServiceHandler) preview(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Origin    string    `json:"origin"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	h.do(w, r, func(shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
		return h.Service.RegisterPreview(r.Context(), shopID, id, h.cred(r), b.Origin, b.ExpiresAt)
	})
}

func (h *DesignServiceHandler) submit(w http.ResponseWriter, r *http.Request) {
	h.do(w, r, func(shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
		return h.Service.Submit(r.Context(), shopID, id, h.cred(r))
	})
}

func (h *DesignServiceHandler) published(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PublishedSHA string `json:"published_sha"`
		Tag          string `json:"tag"`
		DeploymentID string `json:"deployment_id"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	h.do(w, r, func(shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
		return h.Service.ReportPublished(r.Context(), shopID, id, h.cred(r),
			design.PublishedInput{PublishedSHA: b.PublishedSHA, Tag: b.Tag, DeploymentID: b.DeploymentID})
	})
}

func (h *DesignServiceHandler) failed(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	h.do(w, r, func(shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
		return h.Service.ReportFailed(r.Context(), shopID, id, h.cred(r), b.Code, b.Detail)
	})
}

func (h *DesignServiceHandler) putState(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var b struct {
		MainSHA      string `json:"main_sha"`
		CoreVersion  string `json:"core_version"`
		FloorVersion string `json:"design_floor_version"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	st, err := h.Service.PutSiteState(r.Context(), shopID, chi.URLParam(r, "site"),
		design.SiteStateInput{MainSHA: b.MainSHA, CoreVersion: b.CoreVersion, FloorVersion: b.FloorVersion})
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, siteStateJSON(design.SiteView{Site: st.Site, State: st}))
}

func (h *DesignServiceHandler) untracked(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var b struct {
		Changes []struct {
			Site         string    `json:"site"`
			CommitSHA    string    `json:"commit_sha"`
			CommittedAt  time.Time `json:"committed_at"`
			Subject      string    `json:"subject"`
			FilesChanged int       `json:"files_changed"`
			Additions    int       `json:"additions"`
			Deletions    int       `json:"deletions"`
		} `json:"changes"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	in := make([]design.UntrackedInput, 0, len(b.Changes))
	for _, c := range b.Changes {
		in = append(in, design.UntrackedInput{Site: c.Site, CommitSHA: c.CommitSHA, CommittedAt: c.CommittedAt,
			Subject: c.Subject, FilesChanged: c.FilesChanged, Additions: c.Additions, Deletions: c.Deletions})
	}
	n, err := h.Service.ReportUntracked(r.Context(), shopID, in)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"received": len(in), "added": n})
}

// uploadScreenshot stores one screenshot (design D5/D6/D11). The path decides
// the slot; no free-form file name is accepted. 201 for a new image, 200 when
// the same bytes were already stored for the slot.
func (h *DesignServiceHandler) uploadScreenshot(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "image/png" {
		httpx.WriteError(w, http.StatusUnsupportedMediaType, "invalid_screenshot", "Content-Type must be image/png", nil)
		return
	}
	viewport, err := strconv.Atoi(chi.URLParam(r, "viewport"))
	if err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", "viewport must be 390 or 1280", nil)
		return
	}
	// One byte over the limit is enough to know it is too large.
	r.Body = http.MaxBytesReader(w, r.Body, design.MaxScreenshotBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "invalid_screenshot", "the image is larger than 2 MiB", nil)
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "the request body could not be read", nil)
		return
	}
	in := design.UploadInput{
		ShopID: shopID, RequestID: id, Phase: chi.URLParam(r, "phase"), Role: chi.URLParam(r, "role"),
		Viewport: viewport, ViewKey: chi.URLParam(r, "viewKey"), Body: body,
		ClaimedSHA256: r.Header.Get("X-Content-SHA256"), Manifest: r.Header.Get("X-Capture-Manifest"),
	}
	if v := r.Header.Get("X-Diff-Ppm"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil {
			httpx.WriteError(w, http.StatusUnprocessableEntity, "invalid_screenshot", "X-Diff-Ppm must be an integer", nil)
			return
		}
		in.DiffPPM = &n
	}
	row, replay, err := h.Service.UploadScreenshot(r.Context(), in)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, status, screenshotJSON(row))
}
