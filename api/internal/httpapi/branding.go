package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/ent"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// Per-shop admin branding (change admin-api-public-exposure, design D8): the
// look of the SHARED merchant admin SPA - a logo and two colors. It is plain
// data: nothing in the API branches on which front end reads it, and a custom
// admin front end (Bouquet's) may ignore it entirely.

var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// maxLogoURLLen bounds the stored logo URL.
const maxLogoURLLen = 500

// parseAdminBranding validates an admin_branding value and returns what to
// store: a JSON object holding only the non-empty keys. The value REPLACES the
// stored branding (PUT semantics); JSON null or {} clears it. Colors are
// normalized to lowercase #rrggbb.
func parseAdminBranding(raw json.RawMessage, imagePrefixes []string) (json.RawMessage, []map[string]any) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &in); err != nil {
		return nil, []map[string]any{{"pointer": "/admin_branding", "message": "must be an object"}}
	}
	out := map[string]string{}
	var details []map[string]any
	bad := func(key, msg string) {
		details = append(details, map[string]any{"pointer": "/admin_branding/" + key, "message": msg})
	}
	for key, v := range in {
		switch key {
		case "logo_url", "primary_color", "accent_color":
		default:
			bad(key, "unknown field")
			continue
		}
		var str *string
		if err := json.Unmarshal(v, &str); err != nil {
			bad(key, "must be a string")
			continue
		}
		if str == nil || strings.TrimSpace(*str) == "" {
			continue // empty / null clears the key
		}
		val := strings.TrimSpace(*str)
		if key == "logo_url" {
			u, err := url.Parse(val)
			switch {
			case len(val) > maxLogoURLLen:
				bad(key, "must be at most 500 characters")
			case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
				bad(key, "must be an absolute http(s) URL")
			case !cms.ImageURLAllowed(imagePrefixes, val):
				bad(key, "must be on the configured image domain")
			default:
				out[key] = val
			}
			continue
		}
		if !hexColor.MatchString(val) {
			bad(key, "must be a #RRGGBB color")
			continue
		}
		out[key] = strings.ToLower(val)
	}
	if len(details) > 0 {
		return nil, details
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, []map[string]any{{"pointer": "/admin_branding", "message": "cannot encode"}}
	}
	return b, nil
}

// brandingJSON is the stable response shape: all three keys always present,
// "" meaning unset.
func brandingJSON(stored json.RawMessage) map[string]string {
	out := map[string]string{"logo_url": "", "primary_color": "", "accent_color": ""}
	var m map[string]string
	if len(stored) > 0 && json.Unmarshal(stored, &m) == nil {
		for k := range out {
			out[k] = m[k]
		}
	}
	return out
}

// BrandingHandler serves admin branding reads.
type BrandingHandler struct {
	Client   *ent.Client
	Registry OriginRegistry
	Authz    *AuthzMW
	Log      *slog.Logger
}

// MountShopAdmin registers the authenticated, shop-scoped read under
// /admin/shops/{shopID}. Any member of the shop may read it (cross-shop guard,
// no permission node): the SPA applies it right after sign-in, before it knows
// what the user may do.
func (h *BrandingHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopAccess()).Get("/branding", h.getForShop)
}

// MountPublic registers the UNAUTHENTICATED GET /admin/branding. The shop is
// resolved from the request's registered admin origin/host (design D4); a host
// that is not a registered admin origin gets 404 and the SPA falls back to the
// platform look. What leaks is a shop name, logo and two colors to anyone who
// already knows that admin domain - exactly what its login page shows anyway.
func (h *BrandingHandler) MountPublic(r chi.Router) {
	r.Get("/branding", h.getByHost)
}

func (h *BrandingHandler) write(w http.ResponseWriter, s *ent.Shop, withID bool) {
	body := map[string]any{"name": s.Name, "branding": brandingJSON(s.AdminBranding)}
	if withID {
		body["shop_id"] = s.ID
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *BrandingHandler) getForShop(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	s, err := h.Client.Shop.Query().Where(entshop.IDEQ(shopID)).Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			httpx.NotFound(w)
			return
		}
		h.Log.Error("branding: load shop", "err", err)
		httpx.Internal(w)
		return
	}
	h.write(w, s, true)
}

func (h *BrandingHandler) getByHost(w http.ResponseWriter, r *http.Request) {
	shopID, ok := boundAdminShop(r, h.Registry)
	if !ok {
		httpx.NotFound(w)
		return
	}
	s, err := h.Client.Shop.Query().Where(entshop.IDEQ(shopID)).Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			httpx.NotFound(w)
			return
		}
		h.Log.Error("branding: load shop", "err", err)
		httpx.Internal(w)
		return
	}
	h.write(w, s, false)
}
