package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// Shared helpers of the design-history routes (change design-history). The
// human routes (design_admin.go) and the machine routes (design_service.go)
// are separate route families and share only serialization and error mapping.

// designNoSniff is applied to every design route: history text (prompts,
// failure messages, file paths) is user- or service-supplied data, so the API
// answers application/json with nosniff and never anything a browser could
// interpret as markup.
func designNoSniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// writeDesignError maps a domain error to the envelope.
func writeDesignError(w http.ResponseWriter, log *slog.Logger, err error) {
	var de *design.Error
	if errors.As(err, &de) {
		httpx.WriteError(w, de.Status, de.Code, de.Message, de.Details)
		return
	}
	log.Error("design", "err_class", "internal")
	httpx.Internal(w)
}

func requestIDParam(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "requestID"))
	return id, err == nil
}

func ts(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func excerpt(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func rawJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// requestCommon is the part of a request both route families expose.
func requestCommon(r *ent.DesignRequest) map[string]any {
	return map[string]any{
		"id": r.ID.String(), "site": r.Site, "kind": r.Kind, "status": r.Status,
		"requested_by_user_id": r.RequestedByUserID, "prompt_sha256": r.PromptSha256, "prompt_flags": r.PromptFlags,
		"base_sha": r.BaseSha, "head_sha": r.HeadSha, "candidate_sha": r.CandidateSha, "published_sha": r.PublishedSha,
		"branch": r.Branch, "core_version": r.CoreVersion, "design_floor_version": r.DesignFloorVersion,
		"preview_registered_at": ts(r.PreviewRegisteredAt), "preview_expires_at": ts(r.PreviewExpiresAt),
		"approved_by_user_id": r.ApprovedByUserID, "approved_at": ts(r.ApprovedAt), "approved_head_sha": r.ApprovedHeadSha,
		"approval_expires_at": ts(r.ApprovalExpiresAt), "publish_tag": r.PublishTag,
		"published_at": ts(r.PublishedAt), "published_tag": r.PublishedTag, "deployment_id": r.DeploymentID,
		"restores_request_id": uuidPtr(r.RestoresRequestID), "reverted_by_request_id": uuidPtr(r.RevertedByRequestID),
		"failure_code": r.FailureCode, "rev": r.Rev, "created_at": r.CreatedAt.UTC(), "updated_at": r.UpdatedAt.UTC(),
	}
}

func uuidPtr(u *uuid.UUID) any {
	if u == nil {
		return nil
	}
	return u.String()
}

// designSummaryJSON is a list row: no prompt body, only an excerpt.
func designSummaryJSON(r *ent.DesignRequest) map[string]any {
	m := requestCommon(r)
	ex := ""
	if r.PromptText != nil {
		ex = excerpt(*r.PromptText, 120)
	}
	m["prompt_excerpt"] = ex
	return m
}

// designDetailJSON is the full request for people. Fields the service
// reported (prompt aside) are the service's claims, not platform facts.
func designDetailJSON(r *ent.DesignRequest) map[string]any {
	m := requestCommon(r)
	m["prompt_text"] = r.PromptText
	m["diff_summary"] = rawJSON(r.DiffSummary)
	m["guard"] = rawJSON(r.Guard)
	m["preview_origin"] = r.PreviewOrigin
	m["rejected_by_user_id"] = r.RejectedByUserID
	m["rejected_at"] = ts(r.RejectedAt)
	m["reject_reason"] = r.RejectReason
	m["change_note"] = r.ChangeNote
	m["failure_detail"] = r.FailureDetail
	return m
}

func designEventJSON(e *ent.DesignRequestEvent) map[string]any {
	return map[string]any{
		"seq": e.Seq, "kind": e.Kind, "from_status": e.FromStatus, "to_status": e.ToStatus,
		"actor_type": e.ActorType, "actor_user_id": e.ActorUserID, "actor_credential_id": e.ActorCredentialID,
		"head_sha": e.HeadSha, "detail": rawJSON(e.Detail), "hash": e.Hash, "created_at": e.CreatedAt.UTC(),
	}
}

func queryPage(r *http.Request) (page, size int) {
	page, size = 1, 20
	if v := r.URL.Query().Get("page"); v != "" {
		if n := atoiOr(v, 0); n >= 1 {
			page = n
		}
	}
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n := atoiOr(v, 0); n >= 1 && n <= 100 {
			size = n
		}
	}
	return
}

func atoiOr(s string, def int) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' || n > 1_000_000 {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// screenshotJSON is the metadata of a stored screenshot: never the object key
// (a storage detail) and never a URL (those are minted per read).
func screenshotJSON(r *ent.DesignScreenshot) map[string]any {
	return map[string]any{
		"id": r.ID.String(), "request_id": r.RequestID.String(), "phase": r.Phase, "site": r.Site, "role": r.Role,
		"viewport": r.Viewport, "view_key": r.ViewKey, "attempt": r.Attempt, "commit_sha": r.CommitSha,
		"content_type": r.ContentType, "byte_size": r.ByteSize, "width": r.Width, "height": r.Height,
		"sha256": r.Sha256, "diff_ppm": r.DiffPpm, "capture_manifest": rawJSON(r.CaptureManifest),
		"purged_at": ts(r.PurgedAt), "created_at": r.CreatedAt.UTC(),
	}
}
