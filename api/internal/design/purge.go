package design

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/lib/pq"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designrequest"
	"ksdevworks/ecommerce/api/internal/ent/designrequestevent"
	"ksdevworks/ecommerce/api/internal/ent/designscreenshot"
	"ksdevworks/ecommerce/api/internal/ent/designsitestate"
	"ksdevworks/ecommerce/api/internal/ent/designuntrackedchange"
)

// ObjectDeleter removes stored screenshot objects (slice 2). It is optional:
// without one the purge still marks and deletes rows and reports how many
// objects it could not remove.
type ObjectDeleter interface {
	Delete(ctx context.Context, key string) error
}

// PurgeConfig is the retention policy (design D21).
type PurgeConfig struct {
	PromptRetention                time.Duration
	ScreenshotRetention            time.Duration // screenshots of published requests
	ScreenshotRetentionUnpublished time.Duration // rejected, failed, cancelled
	AuditRetention                 time.Duration // requests and events
	UntrackedRetention             time.Duration
	CredentialRetention            time.Duration // after revocation or expiry
	Shop                           int           // 0 = every shop
	Batch                          int           // 0 = 1000
}

// DefaultPurgeConfig returns the decided retention (Q1).
func DefaultPurgeConfig() PurgeConfig {
	d := 24 * time.Hour
	return PurgeConfig{
		PromptRetention: 365 * d, ScreenshotRetention: 365 * d, ScreenshotRetentionUnpublished: 30 * d,
		AuditRetention: 730 * d, UntrackedRetention: 730 * d, CredentialRetention: 90 * d,
	}
}

// PurgeReport counts what a purge did (or would do). It carries counts and time
// ranges only.
type PurgeReport struct {
	Prompts, Screenshots, Requests, Untracked, Credentials int
	OldestRequest, NewestRequest                           *time.Time
	ObjectsDeleted, ObjectsFailed                          int
}

// Purger applies the retention policy.
type Purger struct {
	Client *ent.Client
	DB     *sql.DB
	Store  ObjectDeleter
	Log    *slog.Logger
	Now    func() time.Time
}

func (p *Purger) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

const terminalList = `'published','rejected','failed','cancelled','reverted'`

// retentionTx runs fn in a transaction that is allowed past the append-only
// triggers. SET LOCAL semantics: the permission ends with the transaction.
func (p *Purger) retentionTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('design.retention', 'on', true)`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Purge applies (apply=true) or only counts (apply=false) the retention policy.
func (p *Purger) Purge(ctx context.Context, cfg PurgeConfig, apply bool) (*PurgeReport, error) {
	if cfg.Batch <= 0 {
		cfg.Batch = 1000
	}
	now := p.now()
	rep := &PurgeReport{}
	var keys []string

	// 1. prompts of finished requests.
	promptCut := now.Add(-cfg.PromptRetention)
	if !apply {
		if err := p.DB.QueryRowContext(ctx, `SELECT count(*) FROM design_requests WHERE prompt_text IS NOT NULL AND status IN (`+terminalList+`) AND created_at < $1 AND ($2 = 0 OR shop_id = $2)`,
			promptCut, cfg.Shop).Scan(&rep.Prompts); err != nil {
			return rep, err
		}
	} else {
		for {
			var n int64
			err := p.retentionTx(ctx, func(tx *sql.Tx) error {
				res, err := tx.ExecContext(ctx, `WITH c AS (SELECT id FROM design_requests WHERE prompt_text IS NOT NULL AND status IN (`+terminalList+`) AND created_at < $1 AND ($2 = 0 OR shop_id = $2) LIMIT $3)
UPDATE design_requests r SET prompt_text = NULL FROM c WHERE r.id = c.id`, promptCut, cfg.Shop, cfg.Batch)
				if err != nil {
					return err
				}
				n, _ = res.RowsAffected()
				return nil
			})
			if err != nil {
				return rep, err
			}
			rep.Prompts += int(n)
			if n < int64(cfg.Batch) {
				break
			}
		}
	}

	// 2. screenshots: rows are kept, the object goes and purged_at is set.
	pubCut, unpubCut := now.Add(-cfg.ScreenshotRetention), now.Add(-cfg.ScreenshotRetentionUnpublished)
	shotWhere := `s.purged_at IS NULL AND ($3 = 0 OR s.shop_id = $3) AND (
  (r.status IN ('published','reverted') AND s.created_at < $1) OR (r.status IN ('rejected','failed','cancelled') AND s.created_at < $2))`
	if !apply {
		if err := p.DB.QueryRowContext(ctx, `SELECT count(*) FROM design_screenshots s JOIN design_requests r ON r.id = s.request_id WHERE `+shotWhere,
			pubCut, unpubCut, cfg.Shop).Scan(&rep.Screenshots); err != nil {
			return rep, err
		}
	} else {
		for {
			var got []string
			err := p.retentionTx(ctx, func(tx *sql.Tx) error {
				rows, err := tx.QueryContext(ctx, `WITH c AS (SELECT s.id FROM design_screenshots s JOIN design_requests r ON r.id = s.request_id WHERE `+shotWhere+` LIMIT $4)
UPDATE design_screenshots s SET purged_at = $5 FROM c WHERE s.id = c.id RETURNING s.object_key`, pubCut, unpubCut, cfg.Shop, cfg.Batch, now)
				if err != nil {
					return err
				}
				defer func() { _ = rows.Close() }()
				for rows.Next() {
					var k string
					if err := rows.Scan(&k); err != nil {
						return err
					}
					got = append(got, k)
				}
				return rows.Err()
			})
			if err != nil {
				return rep, err
			}
			rep.Screenshots += len(got)
			keys = append(keys, got...)
			if len(got) < cfg.Batch {
				break
			}
		}
	}

	// 3. requests (and, by cascade, their events and screenshot rows) that are
	// finished and older than the audit retention. A request still referenced by
	// a newer revert request waits for it.
	auditCut := now.Add(-cfg.AuditRetention)
	reqWhere := `r.status IN (` + terminalList + `) AND r.created_at < $1 AND ($2 = 0 OR r.shop_id = $2)
AND NOT EXISTS (SELECT 1 FROM design_requests o WHERE o.restores_request_id = r.id OR o.reverted_by_request_id = r.id)`
	if !apply {
		if err := p.DB.QueryRowContext(ctx, `SELECT count(*), min(r.created_at), max(r.created_at) FROM design_requests r WHERE `+reqWhere,
			auditCut, cfg.Shop).Scan(&rep.Requests, &rep.OldestRequest, &rep.NewestRequest); err != nil {
			return rep, err
		}
	} else {
		for {
			var ids []string
			err := p.retentionTx(ctx, func(tx *sql.Tx) error {
				rows, err := tx.QueryContext(ctx, `SELECT r.id::text, r.created_at FROM design_requests r WHERE `+reqWhere+` ORDER BY r.created_at LIMIT $3`, auditCut, cfg.Shop, cfg.Batch)
				if err != nil {
					return err
				}
				var created []time.Time
				for rows.Next() {
					var id string
					var at time.Time
					if err := rows.Scan(&id, &at); err != nil {
						_ = rows.Close()
						return err
					}
					ids = append(ids, id)
					created = append(created, at)
				}
				_ = rows.Close()
				if len(ids) == 0 {
					return nil
				}
				krows, err := tx.QueryContext(ctx, `SELECT object_key FROM design_screenshots WHERE request_id = ANY($1::uuid[]) AND purged_at IS NULL`, pq.Array(ids))
				if err != nil {
					return err
				}
				for krows.Next() {
					var k string
					if err := krows.Scan(&k); err != nil {
						_ = krows.Close()
						return err
					}
					keys = append(keys, k)
				}
				_ = krows.Close()
				if _, err := tx.ExecContext(ctx, `DELETE FROM design_requests WHERE id = ANY($1::uuid[])`, pq.Array(ids)); err != nil {
					return err
				}
				if rep.OldestRequest == nil || created[0].Before(*rep.OldestRequest) {
					t := created[0]
					rep.OldestRequest = &t
				}
				if rep.NewestRequest == nil || created[len(created)-1].After(*rep.NewestRequest) {
					t := created[len(created)-1]
					rep.NewestRequest = &t
				}
				return nil
			})
			if err != nil {
				return rep, err
			}
			rep.Requests += len(ids)
			if len(ids) < cfg.Batch {
				break
			}
		}
	}

	// 4. untracked changes and 5. credentials.
	simple := []struct {
		dst   *int
		count string
		del   string
		args  []any
	}{
		{&rep.Untracked,
			`SELECT count(*) FROM design_untracked_changes WHERE detected_at < $1 AND ($2 = 0 OR shop_id = $2)`,
			`DELETE FROM design_untracked_changes WHERE id IN (SELECT id FROM design_untracked_changes WHERE detected_at < $1 AND ($2 = 0 OR shop_id = $2) LIMIT $3)`,
			[]any{now.Add(-cfg.UntrackedRetention), cfg.Shop}},
		{&rep.Credentials,
			`SELECT count(*) FROM design_service_credentials WHERE (revoked_at < $1 OR expires_at < $1) AND ($2 = 0 OR shop_id = $2)`,
			`DELETE FROM design_service_credentials WHERE id IN (SELECT id FROM design_service_credentials WHERE (revoked_at < $1 OR expires_at < $1) AND ($2 = 0 OR shop_id = $2) LIMIT $3)`,
			[]any{now.Add(-cfg.CredentialRetention), cfg.Shop}},
	}
	for _, q := range simple {
		if !apply {
			if err := p.DB.QueryRowContext(ctx, q.count, q.args...).Scan(q.dst); err != nil {
				return rep, err
			}
			continue
		}
		for {
			var n int64
			err := p.retentionTx(ctx, func(tx *sql.Tx) error {
				res, err := tx.ExecContext(ctx, q.del, append(append([]any{}, q.args...), cfg.Batch)...)
				if err != nil {
					return err
				}
				n, _ = res.RowsAffected()
				return nil
			})
			if err != nil {
				return rep, err
			}
			*q.dst += int(n)
			if n < int64(cfg.Batch) {
				break
			}
		}
	}

	// Objects go after the commits, best effort: a failure leaves an orphan
	// object (not an incorrect row) and a WARN without any URL.
	p.deleteObjects(ctx, keys, rep)
	return rep, nil
}

func (p *Purger) deleteObjects(ctx context.Context, keys []string, rep *PurgeReport) {
	if p.Store == nil {
		rep.ObjectsFailed += len(keys)
		return
	}
	for _, k := range keys {
		if err := p.Store.Delete(ctx, k); err != nil {
			rep.ObjectsFailed++
			if p.Log != nil {
				p.Log.Warn("design purge: object delete failed", "err_class", "storage")
			}
			continue
		}
		rep.ObjectsDeleted++
	}
}

// AllForShop deletes the whole design history of one shop (decommissioning or a
// personal-data request). It touches no other shop.
func (p *Purger) AllForShop(ctx context.Context, shopID int, apply bool) (*PurgeReport, error) {
	if shopID <= 0 {
		return nil, fmt.Errorf("design: shop id is required")
	}
	rep := &PurgeReport{}
	if err := p.DB.QueryRowContext(ctx, `SELECT count(*) FROM design_requests WHERE shop_id = $1`, shopID).Scan(&rep.Requests); err != nil {
		return rep, err
	}
	if err := p.DB.QueryRowContext(ctx, `SELECT count(*) FROM design_untracked_changes WHERE shop_id = $1`, shopID).Scan(&rep.Untracked); err != nil {
		return rep, err
	}
	if !apply {
		return rep, nil
	}
	var keys []string
	err := p.retentionTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT object_key FROM design_screenshots WHERE shop_id = $1 AND purged_at IS NULL`, shopID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				_ = rows.Close()
				return err
			}
			keys = append(keys, k)
		}
		_ = rows.Close()
		// Self references first, then the rows (events and screenshots cascade).
		if _, err := tx.ExecContext(ctx, `UPDATE design_requests SET restores_request_id = NULL, reverted_by_request_id = NULL WHERE shop_id = $1 AND (restores_request_id IS NOT NULL OR reverted_by_request_id IS NOT NULL)`, shopID); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM design_requests WHERE shop_id = $1`,
			`DELETE FROM design_untracked_changes WHERE shop_id = $1`,
			`DELETE FROM design_site_state WHERE shop_id = $1`,
		} {
			if _, err := tx.ExecContext(ctx, q, shopID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	p.deleteObjects(ctx, keys, rep)
	return rep, nil
}

// ShopExport is the data of one shop's design history, for export. It holds no
// signed URLs.
type ShopExport struct {
	ShopID      int                          `json:"shop_id"`
	ExportedAt  time.Time                    `json:"exported_at"`
	Requests    []*ent.DesignRequest         `json:"requests"`
	Events      []*ent.DesignRequestEvent    `json:"events"`
	Screenshots []ScreenshotRef              `json:"screenshots"`
	Untracked   []*ent.DesignUntrackedChange `json:"untracked_changes"`
	SiteStates  []*ent.DesignSiteState       `json:"site_states"`
}

// ScreenshotRef is a screenshot's metadata (object key, never a URL).
type ScreenshotRef struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	Phase     string `json:"phase"`
	Role      string `json:"role"`
	Viewport  int16  `json:"viewport"`
	ViewKey   string `json:"view_key"`
	Attempt   int16  `json:"attempt"`
	ObjectKey string `json:"object_key"`
	SHA256    string `json:"sha256"`
	Purged    bool   `json:"purged"`
}

// ExportShop collects the history of one shop.
func (p *Purger) ExportShop(ctx context.Context, shopID int) (*ShopExport, error) {
	out := &ShopExport{ShopID: shopID, ExportedAt: p.now().UTC()}
	var err error
	if out.Requests, err = p.Client.DesignRequest.Query().Where(designrequest.ShopIDEQ(shopID)).Order(ent.Asc(designrequest.FieldCreatedAt)).All(ctx); err != nil {
		return nil, err
	}
	if out.Events, err = p.Client.DesignRequestEvent.Query().Where(designrequestevent.ShopIDEQ(shopID)).
		Order(ent.Asc(designrequestevent.FieldRequestID), ent.Asc(designrequestevent.FieldSeq)).All(ctx); err != nil {
		return nil, err
	}
	shots, err := p.Client.DesignScreenshot.Query().Where(designscreenshot.ShopIDEQ(shopID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range shots {
		out.Screenshots = append(out.Screenshots, ScreenshotRef{
			ID: s.ID.String(), RequestID: s.RequestID.String(), Phase: s.Phase, Role: s.Role, Viewport: s.Viewport,
			ViewKey: s.ViewKey, Attempt: s.Attempt, ObjectKey: s.ObjectKey, SHA256: s.Sha256, Purged: s.PurgedAt != nil,
		})
	}
	if out.Untracked, err = p.Client.DesignUntrackedChange.Query().Where(designuntrackedchange.ShopIDEQ(shopID)).All(ctx); err != nil {
		return nil, err
	}
	out.SiteStates, err = p.Client.DesignSiteState.Query().Where(designsitestate.ShopIDEQ(shopID)).All(ctx)
	return out, err
}
