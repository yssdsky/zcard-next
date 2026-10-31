package fulfillment

import (
	"context"
	"testing"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

func TestEmailRecoveredTokenAndOriginalPasswordFetchCards(t *testing.T) {
	d, cipher, repo := newFulfillData(t)
	_, o := seedPaidOrderWithCards(t, d, cipher, "status", 1)
	ctx := context.Background()
	token, _ := orderaccess.NewToken()
	password, _ := crypto.HashPassword("original-password")
	d.Client.Order.UpdateOneID(o.ID).SetUserID(0).SetQueryPasswordHash(password).SetOrderAccessTokenHash(orderaccess.TokenHash(0, o.OrderNo, token)).ExecX(ctx)
	if err := repo.FulfillOrder(ctx, o.OrderNo); err != nil {
		t.Fatal(err)
	}
	svc := NewStoreDeliveryService(repo)
	for _, request := range []*storefrontv1.FetchDeliveryRequest{
		{OrderNo: o.OrderNo, OrderAccessToken: token},
		{OrderNo: o.OrderNo, QueryPassword: "original-password"},
		{OrderNo: o.OrderNo, QueryPassword: "original-password", OrderAccessToken: "stale-token"},
	} {
		got, err := svc.FetchDelivery(ctx, request)
		if err != nil || len(got.GetItems()) != 1 || got.Items[0].Content != "CARD-SECRET-0" || got.Items[0].Masked {
			t.Fatal("valid recovery/password could not read delivered cards", got, err)
		}
	}
	for _, request := range []*storefrontv1.FetchDeliveryRequest{
		{OrderNo: o.OrderNo, OrderAccessToken: "wrong-token"},
		{OrderNo: o.OrderNo, QueryPassword: "wrong-password"},
		{OrderNo: "MISSING", OrderAccessToken: token},
	} {
		if _, err := svc.FetchDelivery(ctx, request); err == nil {
			t.Fatal("unauthorized card fetch succeeded")
		}
	}
	foreign := tenancy.WithContext(ctx, tenancy.Context{SubsiteID: 9})
	if _, err := svc.FetchDelivery(foreign, &storefrontv1.FetchDeliveryRequest{OrderNo: o.OrderNo, OrderAccessToken: token}); err == nil {
		t.Fatal("recovered token crossed tenant boundary")
	}
	other := d.Client.Order.Create().SetOrderNo("OTHER-TOKEN-ORDER").SaveX(ctx)
	if _, err := svc.FetchDelivery(ctx, &storefrontv1.FetchDeliveryRequest{OrderNo: other.OrderNo, OrderAccessToken: token}); err == nil {
		t.Fatal("recovered token crossed order boundary")
	}
	// Even a valid credential must not expose delivery rows before payment.
	d.Client.Order.UpdateOneID(o.ID).SetStatus(order.StatusPendingPayment).ExecX(ctx)
	got, err := svc.FetchDelivery(ctx, &storefrontv1.FetchDeliveryRequest{OrderNo: o.OrderNo, OrderAccessToken: token})
	if err != nil || len(got.GetItems()) != 0 {
		t.Fatal("unpaid order exposed card content", got, err)
	}
}
