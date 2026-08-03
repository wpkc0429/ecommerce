package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/ent"
	entcred "ksdevworks/ecommerce/api/internal/ent/shoppaymentprovidercredential"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
)

// PaymentCredentialsHandler lets a merchant manage its OWN ecpay credentials
// (change ecpay-per-shop-credentials, design D3/D6). The two secret fields
// (HashKey/HashIV) are write-only: GET reports only whether each is set, PUT
// is the only way to change them, and leaving one blank on an update keeps
// whatever is already stored — first-time setup still requires both, since
// there is nothing to keep.
//
// Guarded by payment_credential.manage, granted only to merchant_owner (design
// D6) — configuring a payment account is an integration action, not content
// editing, the same reasoning publishable_key.* already uses.
type PaymentCredentialsHandler struct {
	Client *ent.Client
	Authz  *AuthzMW
	Log    *slog.Logger
	Box    *cryptbox.Box
}

// MountShopAdmin registers the merchant-scoped credentials routes under
// /admin/shops/{shopID}. Flat pattern on the existing sub-router (same
// reasoning as ShopSettingsHandler.MountShopAdmin).
func (h *PaymentCredentialsHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("payment_credential.manage")).Get("/ecpay-credentials", h.get)
	r.With(h.Authz.RequireShopPermission("payment_credential.manage")).Put("/ecpay-credentials", h.put)
}

// credentialsJSON never includes HashKey/HashIV plaintext or ciphertext —
// only whether each is set (design D3/D6). row is nil when the shop has not
// configured ecpay yet.
func credentialsJSON(shopID int, row *ent.ShopPaymentProviderCredential) map[string]any {
	out := map[string]any{
		"shop_id":    shopID,
		"provider":   ecpay.ProviderName,
		"configured": row != nil,
	}
	if row == nil {
		return out
	}
	out["merchant_id"] = row.MerchantID
	out["choose_payment"] = row.ChoosePayment
	out["hash_key_configured"] = len(row.HashKeyCiphertext) > 0
	out["hash_iv_configured"] = len(row.HashIvCiphertext) > 0
	out["updated_at"] = row.UpdatedAt
	return out
}

func (h *PaymentCredentialsHandler) queryOwn(r *http.Request, shopID int) (*ent.ShopPaymentProviderCredential, error) {
	return h.Client.ShopPaymentProviderCredential.Query().
		Where(entcred.ShopIDEQ(shopID), entcred.ProviderEQ(ecpay.ProviderName)).
		Only(r.Context())
}

func (h *PaymentCredentialsHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	row, err := h.queryOwn(r, shopID)
	if err != nil && !ent.IsNotFound(err) {
		h.Log.Error("query ecpay credentials", "err", err)
		httpx.Internal(w)
		return
	}
	if ent.IsNotFound(err) {
		row = nil
	}
	httpx.WriteJSON(w, http.StatusOK, credentialsJSON(shopID, row))
}

// putECPayCredentialsRequest: HashKey/HashIV empty ("") means "leave
// unchanged" on an update — there is no way to distinguish "omitted" from
// "empty string" for a plain string field, and there is no legitimate reason
// to ever set either to an empty string, so the two cases can safely share
// one meaning (design D6).
type putECPayCredentialsRequest struct {
	MerchantID    string  `json:"merchant_id"`
	HashKey       string  `json:"hash_key"`
	HashIV        string  `json:"hash_iv"`
	ChoosePayment *string `json:"choose_payment"`
}

func (h *PaymentCredentialsHandler) put(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)

	var body putECPayCredentialsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.MerchantID) == "" {
		httpx.Unprocessable(w, "merchant_id is required",
			[]map[string]any{{"pointer": "/merchant_id", "message": "required"}})
		return
	}

	existing, err := h.queryOwn(r, shopID)
	if err != nil && !ent.IsNotFound(err) {
		h.Log.Error("query ecpay credentials", "err", err)
		httpx.Internal(w)
		return
	}
	hasExisting := err == nil

	if !hasExisting && (body.HashKey == "" || body.HashIV == "") {
		httpx.Unprocessable(w, "hash_key and hash_iv are required the first time credentials are set",
			[]map[string]any{{"pointer": "/hash_key", "message": "required on first setup"}})
		return
	}

	var row *ent.ShopPaymentProviderCredential
	if hasExisting {
		row, err = h.updateExisting(r, existing, body)
	} else {
		row, err = h.createNew(r, shopID, body)
	}
	if err != nil {
		h.Log.Error("save ecpay credentials", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, credentialsJSON(shopID, row))
}

func (h *PaymentCredentialsHandler) updateExisting(r *http.Request, existing *ent.ShopPaymentProviderCredential, body putECPayCredentialsRequest) (*ent.ShopPaymentProviderCredential, error) {
	upd := h.Client.ShopPaymentProviderCredential.UpdateOneID(existing.ID).SetMerchantID(body.MerchantID)
	if body.HashKey != "" {
		ct, err := h.Box.Encrypt(body.HashKey)
		if err != nil {
			return nil, err
		}
		upd = upd.SetHashKeyCiphertext(ct)
	}
	if body.HashIV != "" {
		ct, err := h.Box.Encrypt(body.HashIV)
		if err != nil {
			return nil, err
		}
		upd = upd.SetHashIvCiphertext(ct)
	}
	if body.ChoosePayment != nil {
		if *body.ChoosePayment == "" {
			upd = upd.ClearChoosePayment()
		} else {
			upd = upd.SetChoosePayment(*body.ChoosePayment)
		}
	}
	return upd.Save(r.Context())
}

func (h *PaymentCredentialsHandler) createNew(r *http.Request, shopID int, body putECPayCredentialsRequest) (*ent.ShopPaymentProviderCredential, error) {
	hashKeyCT, err := h.Box.Encrypt(body.HashKey)
	if err != nil {
		return nil, err
	}
	hashIVCT, err := h.Box.Encrypt(body.HashIV)
	if err != nil {
		return nil, err
	}
	create := h.Client.ShopPaymentProviderCredential.Create().
		SetShopID(shopID).SetProvider(ecpay.ProviderName).SetMerchantID(body.MerchantID).
		SetHashKeyCiphertext(hashKeyCT).SetHashIvCiphertext(hashIVCT)
	if body.ChoosePayment != nil && *body.ChoosePayment != "" {
		create = create.SetChoosePayment(*body.ChoosePayment)
	}
	return create.Save(r.Context())
}
