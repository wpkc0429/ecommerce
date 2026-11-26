package app

import (
	"context"
	"errors"
	"testing"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
)

// Audit M3: the registry doubles as the allowlist of the customer-supplied
// `provider` field, so production must not contain the mock provider.
func TestPaymentProvidersByEnv(t *testing.T) {
	mock := payment.NewMockProvider("test-secret")
	real := &ecpay.ShopResolver{}
	for _, tc := range []struct {
		env      string
		wantMock bool
	}{
		{config.EnvProduction, false},
		{config.EnvDevelopment, true},
		{config.EnvTest, true},
	} {
		t.Run(tc.env, func(t *testing.T) {
			svc := &payment.Service{Providers: paymentProviders(tc.env, mock, real), DefaultProvider: "ecpay"}
			if _, ok := svc.Providers[real.Name()]; !ok {
				t.Fatalf("%s: real provider %q not registered", tc.env, real.Name())
			}
			_, err := svc.ResolveProvider(context.Background(), mock.Name(), 0)
			var ve *payment.ValidationError
			if tc.wantMock && err != nil {
				t.Fatalf("%s: mock must resolve, got %v", tc.env, err)
			}
			if !tc.wantMock && !errors.As(err, &ve) {
				t.Fatalf("%s: mock must be rejected as a validation error, got %v", tc.env, err)
			}
			// Whatever the environment, an arbitrary name is never accepted.
			if _, err := svc.ResolveProvider(context.Background(), "../mock", 0); !errors.As(err, &ve) {
				t.Fatalf("%s: unknown provider accepted: %v", tc.env, err)
			}
		})
	}
}
