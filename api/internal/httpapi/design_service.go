package httpapi

import (
	"log/slog"
	"net/http"
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
	r.Post("/requests/{requestID}/submit", h.submit)
	r.Post("/requests/{requestID}/published", h.published)
	r.Post("/requests/{requestID}/failed", h.failed)
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
