package payment

import (
	"context"
	"testing"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

func TestRecoveredGuestPaymentQuoteCreateAndRetry(t *testing.T) {
	for _, credential := range []string{"token", "password", "password-with-stale-token"} {
		t.Run(credential, func(t *testing.T) {
			d, repo, _, _, _, _ := newCallbackEnv(t)
			ctx := context.Background()
			o, _ := seedPendingOrder(t, d, "epay", 1000)
			token, _ := orderaccess.NewToken()
			hash, _ := crypto.HashPassword("original-password")
			d.Client.Order.UpdateOneID(o.ID).SetUserID(0).SetContact("guest@example.test").SetQueryPasswordHash(hash).SetOrderAccessTokenHash(orderaccess.TokenHash(0, o.OrderNo, token)).ExecX(ctx)
			svc := NewStorePaymentService(repo, d)
			request := &storefrontv1.PaymentQuoteRequest{OrderNo: o.OrderNo, Channel: "epay"}
			if credential == "token" {
				request.OrderAccessToken = token
			} else {
				request.QueryPassword = "original-password"
				if credential == "password-with-stale-token" {
					request.OrderAccessToken = "stale-token"
				}
			}
			quote, err := svc.QuotePayment(ctx, request)
			if err != nil {
				t.Fatal("valid recovered credential could not obtain payment quote", err)
			}
			paymentRequest := &storefrontv1.CreatePaymentRequest{OrderNo: o.OrderNo, Channel: "epay", QuoteKey: quote.QuoteKey, QueryPassword: request.QueryPassword, OrderAccessToken: request.OrderAccessToken}
			first, err := svc.CreatePayment(ctx, paymentRequest)
			if err != nil {
				t.Fatal("valid recovered credential could not create payment", err)
			}
			retry, err := svc.CreatePayment(ctx, paymentRequest)
			if err != nil || retry.GetPaymentId() != first.GetPaymentId() {
				t.Fatal("recovered guest payment retry was not idempotent", err)
			}
			for _, bad := range []*storefrontv1.PaymentQuoteRequest{
				{OrderNo: o.OrderNo, Channel: "epay", OrderAccessToken: "wrong-token"},
				{OrderNo: o.OrderNo, Channel: "epay", QueryPassword: "wrong-password"},
				{OrderNo: "OTHER-ORDER", Channel: "epay", OrderAccessToken: token},
			} {
				if _, err := svc.QuotePayment(ctx, bad); err == nil {
					t.Fatal("unauthorized payment quote succeeded")
				}
				if _, err := svc.CreatePayment(ctx, &storefrontv1.CreatePaymentRequest{OrderNo: bad.OrderNo, Channel: bad.Channel, QueryPassword: bad.QueryPassword, OrderAccessToken: bad.OrderAccessToken}); err == nil {
					t.Fatal("unauthorized payment creation succeeded")
				}
			}
			foreign := tenancy.WithContext(ctx, tenancy.Context{SubsiteID: 9})
			if _, err := svc.QuotePayment(foreign, request); err == nil {
				t.Fatal("recovered guest quoted another tenant's payment")
			}
			if _, err := svc.CreatePayment(foreign, paymentRequest); err == nil {
				t.Fatal("recovered guest created another tenant's payment")
			}
		})
	}
}
