package ecpay

import (
	"context"
	"fmt"
	"time"

	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/ent"
	entcred "ksdevworks/ecommerce/api/internal/ent/shoppaymentprovidercredential"
	"ksdevworks/ecommerce/api/internal/payment"
)

// ShopResolver implements payment.ShopScopedProvider for ecpay (change
// ecpay-per-shop-credentials, design D1/D5). It is the only part of this
// package that touches the database — Provider/New/InitiatePayment/
// VerifyWebhook stay exactly as pure and DB-free as they were before this
// change (ecpay-payment-provider design D1): ShopResolver's whole job is to
// look up one shop's row, decrypt its two secret fields, and hand back an
// ordinary Provider built the same way a hand-configured one always was.
//
// It has to live in this package rather than in payment (which already
// depends on ecpay for New/Config/ProviderName) — the reverse import would be
// a cycle.
//
// CheckoutURL/ClientBackURL/ReturnURLBase are platform-wide (design D7: which
// 綠界 environment the whole deployment talks to); everything else is
// resolved per shop.
type ShopResolver struct {
	Client *ent.Client
	Box    *cryptbox.Box

	CheckoutURL   string
	ClientBackURL string
	// ReturnURLBase is the platform-configured base; ResolveForShop appends
	// "/{shopID}" to it (design D2) — 綠界 only ever echoes back whatever
	// ReturnURL we hand it at InitiatePayment time, so the URL is entirely
	// ours to shape and needs no 綠界-side configuration.
	ReturnURLBase string

	// Now is threaded through to New (nil means time.Now).
	Now func() time.Time
}

func (r *ShopResolver) Name() string { return ProviderName }

// ResolveForShop looks up shopID's own ecpay credentials, decrypts the secret
// fields, and constructs a Provider instance scoped to exactly that shop.
// Returns payment.ErrProviderNotConfigured if shopID has never set up ecpay —
// callers MUST NOT substitute any other shop's credentials or a platform
// default (design D5/Non-Goals).
func (r *ShopResolver) ResolveForShop(ctx context.Context, shopID int) (payment.Provider, error) {
	row, err := r.Client.ShopPaymentProviderCredential.Query().
		Where(entcred.ShopIDEQ(shopID), entcred.ProviderEQ(ProviderName)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, payment.ErrProviderNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("ecpay: query shop credentials: %w", err)
	}

	hashKey, err := r.Box.Decrypt(row.HashKeyCiphertext)
	if err != nil {
		return nil, fmt.Errorf("ecpay: decrypt hash key: %w", err)
	}
	hashIV, err := r.Box.Decrypt(row.HashIvCiphertext)
	if err != nil {
		return nil, fmt.Errorf("ecpay: decrypt hash iv: %w", err)
	}

	var choosePayment string
	if row.ChoosePayment != nil {
		choosePayment = *row.ChoosePayment
	}

	return New(Config{
		MerchantID:    row.MerchantID,
		HashKey:       hashKey,
		HashIV:        hashIV,
		ChoosePayment: choosePayment,
		CheckoutURL:   r.CheckoutURL,
		ClientBackURL: r.ClientBackURL,
		ReturnURL:     fmt.Sprintf("%s/%d", r.ReturnURLBase, shopID),
	}, r.Now), nil
}
