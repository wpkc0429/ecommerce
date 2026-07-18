package order

import "testing"

func TestShippingAddressValidate(t *testing.T) {
	complete := ShippingAddress{
		RecipientName: "王小明",
		Phone:         "0912345678",
		Line1:         "民生東路一段1號",
		City:          "台北市",
		PostalCode:    "104",
		Country:       "TW",
	}
	if err := complete.validate(); err != nil {
		t.Fatalf("expected complete address to validate, got %v", err)
	}

	// Line2 is optional — omitting it must not fail validation.
	withoutLine2 := complete
	withoutLine2.Line2 = ""
	if err := withoutLine2.validate(); err != nil {
		t.Fatalf("expected address without line2 to validate, got %v", err)
	}

	cases := []struct {
		name   string
		mutate func(a *ShippingAddress)
	}{
		{"missing recipient_name", func(a *ShippingAddress) { a.RecipientName = "" }},
		{"missing phone", func(a *ShippingAddress) { a.Phone = "" }},
		{"missing line1", func(a *ShippingAddress) { a.Line1 = "" }},
		{"missing city", func(a *ShippingAddress) { a.City = "" }},
		{"missing postal_code", func(a *ShippingAddress) { a.PostalCode = "" }},
		{"missing country", func(a *ShippingAddress) { a.Country = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := complete
			tc.mutate(&a)
			err := a.validate()
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
}

func TestShippingAddressValidateMultipleMissing(t *testing.T) {
	a := ShippingAddress{Country: "TW"}
	err := a.validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve := err.(*ValidationError)
	if len(ve.Details) != 5 {
		t.Fatalf("expected 5 details (all but country), got %d: %+v", len(ve.Details), ve.Details)
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
