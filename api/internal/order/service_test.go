package order

import (
	"errors"
	"testing"
	"time"
)

func TestShippingAddressValidate(t *testing.T) {
	complete := ShippingAddress{
		RecipientName: "王小明",
		Phone:         "0912345678",
		Line1:         "民生東路一段1號",
		City:          "台北市",
		PostalCode:    "104",
		Country:       "TW",
	}
	if err := complete.Validate(); err != nil {
		t.Fatalf("expected complete address to validate, got %v", err)
	}

	// Line2 is optional — omitting it must not fail validation.
	withoutLine2 := complete
	withoutLine2.Line2 = ""
	if err := withoutLine2.Validate(); err != nil {
		t.Fatalf("expected address without line2 to validate, got %v", err)
	}

	// change guest-checkout (design D8): city/postal_code/country are optional.
	// A Taiwanese single-line address ("收件人／手機／地址") must be accepted as is.
	singleLine := ShippingAddress{
		RecipientName: "王小明",
		Phone:         "0912345678",
		Line1:         "台北市松山區民生東路一段1號",
	}
	if err := singleLine.Validate(); err != nil {
		t.Fatalf("expected single-line address to validate, got %v", err)
	}

	cases := []struct {
		name   string
		mutate func(a *ShippingAddress)
	}{
		{"missing recipient_name", func(a *ShippingAddress) { a.RecipientName = "" }},
		{"missing phone", func(a *ShippingAddress) { a.Phone = "" }},
		{"missing line1", func(a *ShippingAddress) { a.Line1 = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := complete
			tc.mutate(&a)
			err := a.Validate()
			if err == nil {
				t.Fatalf("expected validation error")
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if len(ve.Details) != 1 {
				t.Fatalf("expected exactly one detail, got %d: %+v", len(ve.Details), ve.Details)
			}
		})
	}

	// The relaxed fields alone are never enough.
	for _, name := range []string{"city", "postal_code", "country"} {
		t.Run("optional field alone does not satisfy: "+name, func(t *testing.T) {
			a := ShippingAddress{City: "台北市", PostalCode: "104", Country: "TW"}
			if err := a.Validate(); err == nil {
				t.Fatalf("expected validation error for %s-only address", name)
			}
		})
	}
}

func TestShippingAddressValidateMultipleMissing(t *testing.T) {
	a := ShippingAddress{Country: "TW"}
	err := a.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve := err.(*ValidationError)
	if len(ve.Details) != 3 {
		t.Fatalf("expected 3 details (recipient_name, phone, line1), got %d: %+v", len(ve.Details), ve.Details)
	}
}

func TestNormalizePage(t *testing.T) {
	cases := []struct {
		page, pageSize         int
		wantPage, wantPageSize int
	}{
		{0, 0, 1, 20},
		{-1, -1, 1, 20},
		{1, 10, 1, 10},
		{3, 200, 3, 100},
		{2, 0, 2, 20},
	}
	for _, tc := range cases {
		gotPage, gotPageSize := normalizePage(tc.page, tc.pageSize)
		if gotPage != tc.wantPage || gotPageSize != tc.wantPageSize {
			t.Errorf("normalizePage(%d, %d) = (%d, %d), want (%d, %d)",
				tc.page, tc.pageSize, gotPage, gotPageSize, tc.wantPage, tc.wantPageSize)
		}
	}
}

// ── change order-extras: requested delivery date (design D3) ───────────

func TestParseRequestedDeliveryDateAbsent(t *testing.T) {
	empty := ""
	for name, in := range map[string]*string{"nil": nil, "empty string": &empty} {
		got, err := parseRequestedDeliveryDate(in)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if got != nil {
			t.Fatalf("%s: expected no date, got %v", name, got)
		}
	}
}

func TestParseRequestedDeliveryDateAccepts(t *testing.T) {
	// Relative to today so the test does not rot: tomorrow, and the lower
	// bound itself (UTC yesterday), which must be accepted because the server
	// does not know the shop's timezone.
	now := time.Now().UTC()
	for _, offset := range []int{-1, 0, 1, 400} {
		raw := now.AddDate(0, 0, offset).Format(deliveryDateLayout)
		got, err := parseRequestedDeliveryDate(&raw)
		if err != nil {
			t.Fatalf("%s (offset %d): unexpected error: %v", raw, offset, err)
		}
		if got == nil || got.Format(deliveryDateLayout) != raw {
			t.Fatalf("%s: round-trip mismatch, got %v", raw, got)
		}
	}
}

func TestParseRequestedDeliveryDateRejects(t *testing.T) {
	twoDaysAgo := time.Now().UTC().AddDate(0, 0, -2).Format(deliveryDateLayout)
	cases := map[string]string{
		"slashes":            "2026/08/05",
		"nonexistent day":    "2026-02-31",
		"month 13":           "2026-13-01",
		"timestamp":          "2026-08-05T00:00:00Z",
		"prose":              "next wednesday",
		"two-digit year":     "26-08-05",
		"unambiguously past": twoDaysAgo,
		// The typo this rule actually exists to catch.
		"mistyped year": "2025-08-05",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseRequestedDeliveryDate(&raw)
			if err == nil {
				t.Fatalf("expected %q to be rejected, got %v", raw, got)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected a ValidationError (422), got %T", err)
			}
			if len(ve.Details) != 1 || ve.Details[0].Pointer != "/requested_delivery_date" {
				t.Fatalf("expected the error to point at /requested_delivery_date, got %+v", ve.Details)
			}
		})
	}
}
