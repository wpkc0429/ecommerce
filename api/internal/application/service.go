// Package application implements merchant applications (change
// platform-website): the public intake (submit, e-mail verification), the
// operator review (reject, manual verification, approval that opens the shop
// through provision.Service) and PII retention.
//
// Platform data, not tenant data: an application exists before any shop. Every
// state transition is a conditional UPDATE decided by rows affected.
//
// Nothing here logs an e-mail, name or phone. Logs carry application ids and
// event classes only.
package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/shopapplication"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationtoken"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/provision"
)

// Application statuses.
const (
	StatusPendingVerification = "pending_verification"
	StatusPendingReview       = "pending_review"
	StatusApproved            = "approved"
	StatusRejected            = "rejected"
	StatusExpired             = "expired"
	StatusWithdrawn           = "withdrawn"
)

// Audit event names (shop_application_events.event).
const (
	EventSubmitted         = "submitted"
	EventVerificationSent  = "verification_sent"
	EventVerified          = "verified"
	EventVerifiedManually  = "verified_manually"
	EventApproved          = "approved"
	EventApproveFailed     = "approve_failed"
	EventRejected          = "rejected"
	EventMessageMarkedSent = "message_marked_sent"
	EventExpired           = "expired"
	EventPurged            = "purged"
)

// Risk flags (shop_applications.risk_flags). No PII.
const (
	FlagTooFast          = "filled_too_fast"
	FlagEmailHasAccount  = "email_has_account"
	FlagManuallyVerified = "manually_verified"
)

const (
	// TokenTTL is how long a verification link lives.
	TokenTTL = 24 * time.Hour
	// ResendInterval is the minimum gap between two verification tokens for the
	// same application: also the database backstop when the rate limiter is down.
	ResendInterval = 10 * time.Minute
	// UnverifiedTTL: a pending_verification row older than this is deleted by
	// the purge command.
	UnverifiedTTL = 7 * 24 * time.Hour
)

var (
	// ErrNotFound: no such application (or message).
	ErrNotFound = errors.New("application: not found")
	// ErrTokenInvalid: unknown, expired or used verification token. One error on
	// purpose: the three cases are indistinguishable to the caller.
	ErrTokenInvalid = errors.New("application: invalid token")
	// ErrConflict wraps a state conflict (409).
	ErrConflict = errors.New("application: conflict")
	// ErrExistingAccount: approval would add a membership to an existing
	// back-office account and the caller did not confirm it.
	ErrExistingAccount = fmt.Errorf("%w: owner e-mail already has a back-office account; resend with confirm_existing_account", ErrConflict)
)

func conflict(msg string) error { return fmt.Errorf("%w: %s", ErrConflict, msg) }

// Config is the policy the service needs.
type Config struct {
	WebsiteURL        string
	ConsentVersion    string
	ReapplyCooldown   time.Duration
	DailyCap          int
	RejectedRetention time.Duration
	ApprovedRetention time.Duration
	// IPKey keys the HMAC of the submitting IP (the raw IP is never stored).
	IPKey []byte
}

// Service owns the application lifecycle.
type Service struct {
	Client    *ent.Client
	Notifier  Notifier
	Provision *provision.Service
	Cfg       Config
	Log       *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logWarn(msg string, args ...any) {
	if s.Log != nil {
		s.Log.Warn(msg, args...)
	}
}

// Validate normalizes and checks a public submission. It depends on the input
// alone, so its failures reveal nothing about stored data.
func (s *Service) Validate(in SubmitInput) (*Valid, error) {
	v, err := in.validate(s.Cfg.ConsentVersion)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// DailyCapReached reports whether the rolling 24h count of created
// applications reached the platform cap. It reads the database, so it holds
// when Redis (and with it the rate limiter) is down.
func (s *Service) DailyCapReached(ctx context.Context) (bool, error) {
	if s.Cfg.DailyCap <= 0 {
		return false, nil
	}
	n, err := s.Client.ShopApplication.Query().
		Where(shopapplication.CreatedAtGT(s.now().Add(-24 * time.Hour))).Count(ctx)
	if err != nil {
		return false, err
	}
	return n >= s.Cfg.DailyCap, nil
}

func (s *Service) ipHMAC(ip string) string {
	if ip == "" || len(s.Cfg.IPKey) == 0 {
		return ""
	}
	m := hmac.New(sha256.New, s.Cfg.IPKey)
	_, _ = m.Write([]byte(ip))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) verifyURL(token string) string {
	return s.Cfg.WebsiteURL + "/apply/verify#" + token
}

func addEvent(ctx context.Context, c *ent.Client, appID int, actor *int, event string, detail map[string]any) error {
	b := c.ShopApplicationEvent.Create().SetApplicationID(appID).SetEvent(event)
	if actor != nil {
		b.SetActorUserID(*actor)
	}
	if detail != nil {
		b.SetDetail(detail)
	}
	_, err := b.Save(ctx)
	return err
}

// Submit records a validated application. It returns nil in EVERY non-error
// case — new, duplicate, cooling down, already an account — and the caller
// answers all of them with the same 202; what differs is only what happened
// inside (design D2/D4). An error means infrastructure failure.
func (s *Service) Submit(ctx context.Context, v *Valid, clientIP string) error {
	now := s.now()

	// Always look up the account, whichever branch follows: the same reads and
	// roughly the same work for every outcome.
	hasAccount, err := s.Client.User.Query().Where(user.EmailEQ(v.email)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("application: look up account: %w", err)
	}

	open, err := s.Client.ShopApplication.Query().
		Where(shopapplication.EmailEQ(v.email), shopapplication.StatusIn(StatusPendingVerification, StatusPendingReview)).
		Only(ctx)
	switch {
	case err == nil:
		return s.resendIfDue(ctx, open, now)
	case !ent.IsNotFound(err):
		return fmt.Errorf("application: look up open application: %w", err)
	}

	if s.Cfg.ReapplyCooldown > 0 {
		cooling, err := s.Client.ShopApplication.Query().
			Where(
				shopapplication.EmailEQ(v.email),
				shopapplication.StatusIn(StatusRejected, StatusApproved),
				shopapplication.ReviewedAtGT(now.Add(-s.Cfg.ReapplyCooldown)),
			).Exist(ctx)
		if err != nil {
			return fmt.Errorf("application: cooldown check: %w", err)
		}
		if cooling {
			return nil
		}
	}

	flags := map[string]bool{}
	if v.tooFast {
		flags[FlagTooFast] = true
	}
	if hasAccount {
		flags[FlagEmailHasAccount] = true
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("application: begin: %w", err)
	}
	app, err := tx.ShopApplication.Create().
		SetEmail(v.email).SetContactName(v.contactName).SetPhone(v.phone).
		SetShopName(v.shopName).SetBusinessSummary(v.summary).
		SetConsentVersion(v.consent).SetConsentedAt(now).
		SetRiskFlags(flags).SetSubmittedIPHmac(s.ipHMAC(clientIP)).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			// Lost a race with a concurrent submit for the same e-mail: the
			// partial unique index did its job. Indistinguishable from the
			// duplicate branch.
			return nil
		}
		return fmt.Errorf("application: create: %w", err)
	}
	if err := addEvent(ctx, tx.Client(), app.ID, nil, EventSubmitted, nil); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("application: event: %w", err)
	}
	d, err := s.issueVerificationTx(ctx, tx, app, now)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("application: commit: %w", err)
	}
	s.Notifier.AfterCommit(ctx, d)
	return nil
}

// resendIfDue handles a duplicate submission: only a still-unverified
// application whose last token is at least ResendInterval old gets a new one.
func (s *Service) resendIfDue(ctx context.Context, app *ent.ShopApplication, now time.Time) error {
	if app.Status != StatusPendingVerification {
		return nil
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("application: begin: %w", err)
	}
	// Serialize concurrent duplicates on the application row, then re-check.
	locked, err := tx.ShopApplication.Query().Where(shopapplication.IDEQ(app.ID)).ForUpdate().Only(ctx)
	if err != nil || locked.Status != StatusPendingVerification {
		_ = tx.Rollback()
		if err != nil && !ent.IsNotFound(err) {
			return fmt.Errorf("application: lock: %w", err)
		}
		return nil
	}
	last, err := tx.ShopApplicationToken.Query().Where(shopapplicationtoken.ApplicationIDEQ(app.ID)).
		Order(ent.Desc(shopapplicationtoken.FieldCreatedAt)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		_ = tx.Rollback()
		return fmt.Errorf("application: last token: %w", err)
	}
	if last != nil && now.Sub(last.CreatedAt) < ResendInterval {
		_ = tx.Rollback()
		return nil
	}
	d, err := s.issueVerificationTx(ctx, tx, locked, now)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("application: commit: %w", err)
	}
	s.Notifier.AfterCommit(ctx, d)
	return nil
}

// issueVerificationTx retires earlier tokens, mints a new one and queues the
// verification message.
func (s *Service) issueVerificationTx(ctx context.Context, tx *ent.Tx, app *ent.ShopApplication, now time.Time) (Delivery, error) {
	if _, err := tx.ShopApplicationToken.Update().
		Where(shopapplicationtoken.ApplicationIDEQ(app.ID), shopapplicationtoken.UsedAtIsNil()).
		SetUsedAt(now).Save(ctx); err != nil {
		return Delivery{}, fmt.Errorf("application: retire tokens: %w", err)
	}
	plain, hash, err := account.NewToken()
	if err != nil {
		return Delivery{}, err
	}
	if _, err := tx.ShopApplicationToken.Create().
		SetApplicationID(app.ID).SetTokenHash(hash).SetExpiresAt(now.Add(TokenTTL)).SetCreatedAt(now).
		Save(ctx); err != nil {
		return Delivery{}, fmt.Errorf("application: create token: %w", err)
	}
	d, err := s.Notifier.Notify(ctx, tx.Client(), Notification{
		ApplicationID: app.ID, Kind: KindVerifyEmail, To: app.Email,
		Subject: "請驗證您的開店申請信箱",
		Body: fmt.Sprintf("您好，\n\n我們收到了店名為「%s」的開店申請。請在 24 小時內開啟下列連結以驗證此信箱：\n\n%s\n\n如果這不是您本人的申請，請忽略這封信。\n",
			app.ShopName, s.verifyURL(plain)),
	})
	if err != nil {
		return Delivery{}, fmt.Errorf("application: notify: %w", err)
	}
	if err := addEvent(ctx, tx.Client(), app.ID, nil, EventVerificationSent, map[string]any{"delivery": d.Mode}); err != nil {
		return Delivery{}, fmt.Errorf("application: event: %w", err)
	}
	return d, nil
}

// Verify consumes a verification token and moves the application to
// pending_review. Any failure returns ErrTokenInvalid; concurrent calls with
// one token succeed exactly once.
func (s *Service) Verify(ctx context.Context, plain string) error {
	if plain == "" || len(plain) > 256 {
		return ErrTokenInvalid
	}
	now := s.now()
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("application: begin: %w", err)
	}
	hash := account.HashToken(plain)
	tok, err := tx.ShopApplicationToken.Query().Where(shopapplicationtoken.TokenHashEQ(hash)).Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsNotFound(err) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("application: token lookup: %w", err)
	}
	n, err := tx.ShopApplicationToken.Update().
		Where(shopapplicationtoken.IDEQ(tok.ID), shopapplicationtoken.UsedAtIsNil(), shopapplicationtoken.ExpiresAtGT(now)).
		SetUsedAt(now).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("application: consume token: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return ErrTokenInvalid
	}
	n, err = tx.ShopApplication.Update().
		Where(shopapplication.IDEQ(tok.ApplicationID), shopapplication.StatusEQ(StatusPendingVerification)).
		SetStatus(StatusPendingReview).SetEmailVerifiedAt(now).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("application: verify: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return ErrTokenInvalid
	}
	if err := addEvent(ctx, tx.Client(), tok.ApplicationID, nil, EventVerified, nil); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("application: event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("application: commit: %w", err)
	}
	return nil
}
