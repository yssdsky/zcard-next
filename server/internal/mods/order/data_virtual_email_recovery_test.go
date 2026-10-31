package order

import (
	"context"
	"testing"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

func TestGuestVirtualEmailRecoveryPreservesPassword(t *testing.T) {
	d, uc, _ := newIdemEnv(t)
	cipher, err := inventory.NewCardCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	uc.AccessCipher = cipher
	ctx := context.Background()
	r, err := uc.CreateOrder(ctx, CreateOrderInput{Items: []OrderItemInput{{ProductID: 1, Quantity: 1}}, Contact: "Checkout@example.test", QueryPassword: "original-password"})
	if err != nil {
		t.Fatal(err)
	}
	o := d.Client.Order.Query().OnlyX(ctx)
	if o.OrderAccessTokenHash != "" || o.QueryPasswordHash == "" {
		t.Fatal("fixture must exercise a password-based virtual order")
	}
	svc := NewStoreOrderService(uc, nil)
	mail := &accessRecoveryMail{ready: true}
	svc.SetRecoverySender(mail)
	for _, req := range []*storefrontv1.OrderAccessCodeRequest{{OrderNo: r.OrderNo, Email: "other@example.test"}, {OrderNo: "MISSING", Email: "checkout@example.test"}} {
		if _, err := svc.SendOrderAccessCode(ctx, req); err != nil {
			t.Fatal("non-matching order/email revealed existence", err)
		}
	}
	foreign := tenancy.WithContext(ctx, tenancy.Context{SubsiteID: 9})
	if _, err := svc.SendOrderAccessCode(foreign, &storefrontv1.OrderAccessCodeRequest{OrderNo: r.OrderNo, Email: "checkout@example.test"}); err != nil {
		t.Fatal(err)
	}
	if len(mail.messages) != 0 {
		t.Fatal("mismatch or cross-tenant lookup sent email")
	}
	req := &storefrontv1.OrderAccessCodeRequest{OrderNo: r.OrderNo, Email: "CHECKOUT@example.test"}
	if _, err := svc.SendOrderAccessCode(ctx, req); err != nil {
		t.Fatal(err)
	}
	code := recoveryMailCode(t, mail)
	if _, err := svc.SendOrderAccessCode(ctx, req); err != nil || len(mail.messages) != 1 {
		t.Fatal("cooldown did not bound virtual verification mail", err)
	}
	credential, err := svc.RecoverOrderAccess(ctx, &storefrontv1.OrderAccessRecoveryRequest{OrderNo: r.OrderNo, Email: req.Email, Code: code})
	if err != nil || !orderaccess.VerifyToken(d.Client.Order.GetX(ctx, o.ID).OrderAccessTokenHash, o.SubsiteID, r.OrderNo, credential.GetOrderAccessToken()) {
		t.Fatal("virtual email verification did not issue an order credential", err)
	}
	for _, request := range []*storefrontv1.GetOrderRequest{
		{OrderNo: r.OrderNo, OrderAccessToken: credential.OrderAccessToken},
		{OrderNo: r.OrderNo, QueryPassword: "original-password"},
		{OrderNo: r.OrderNo, QueryPassword: "original-password", OrderAccessToken: "stale-token"},
	} {
		if _, err := svc.GetOrder(ctx, request); err != nil {
			t.Fatal("email recovery invalidated a valid access method", err)
		}
	}
	if _, err := svc.GetOrder(ctx, &storefrontv1.GetOrderRequest{OrderNo: r.OrderNo, QueryPassword: "wrong"}); err == nil {
		t.Fatal("wrong password read a recovered virtual order")
	}
	if _, err := svc.GetOrder(foreign, &storefrontv1.GetOrderRequest{OrderNo: r.OrderNo, OrderAccessToken: credential.OrderAccessToken}); err == nil {
		t.Fatal("credential crossed tenant boundary")
	}
	if _, err := svc.RecoverOrderAccess(ctx, &storefrontv1.OrderAccessRecoveryRequest{OrderNo: r.OrderNo, Email: req.Email, Code: code}); err == nil {
		t.Fatal("consumed code replayed")
	}
}

func TestGuestVirtualRecoveryUsesLegacyEmailAndRejectsMemberOrders(t *testing.T) {
	d, uc, _ := newIdemEnv(t)
	uc.AccessCipher, _ = inventory.NewCardCipher(make([]byte, 32))
	ctx := context.Background()
	guest := d.Client.Order.Create().SetOrderNo("LEGACY-EMAIL").SetContact("12345").SetGuestContact("legacy@example.test").SaveX(ctx)
	member := d.Client.Order.Create().SetOrderNo("MEMBER-EMAIL").SetUserID(7).SetContact("legacy@example.test").SaveX(ctx)
	svc := NewStoreOrderService(uc, nil)
	mail := &accessRecoveryMail{ready: true}
	svc.SetRecoverySender(mail)
	if _, err := svc.SendOrderAccessCode(ctx, &storefrontv1.OrderAccessCodeRequest{OrderNo: member.OrderNo, Email: "legacy@example.test"}); err != nil || len(mail.messages) != 0 {
		t.Fatal("guest recovery exposed a member order", err)
	}
	if _, err := svc.SendOrderAccessCode(ctx, &storefrontv1.OrderAccessCodeRequest{OrderNo: guest.OrderNo, Email: "legacy@example.test"}); err != nil || len(mail.messages) != 1 {
		t.Fatal("legacy checkout email could not receive verification", err)
	}
}
