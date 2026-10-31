package fulfillment

import (
	"context"
	"testing"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	ordermod "github.com/NovaWorks/zcard-next/server/internal/mods/order"
	"github.com/NovaWorks/zcard-next/server/internal/platform/id"
)

func TestImportedCompleteCardSurvivesSKUReservationAndDelivery(t *testing.T) {
	d, cipher, deliveries := newFulfillData(t)
	ctx := context.Background()
	cards := inventory.NewCardRepoImpl(d, cipher)
	p := d.Client.Product.Create().SetName("serial and activation").SetSlug("complete-card").SetPrice(100).SetStatus(1).SaveX(ctx)
	a := d.Client.ProductSku.Create().SetProductID(p.ID).SetName("monthly").SetPrice(100).SetSpecValues(map[string]string{}).SaveX(ctx)
	b := d.Client.ProductSku.Create().SetProductID(p.ID).SetName("annual").SetPrice(1000).SetSpecValues(map[string]string{}).SaveX(ctx)
	values := map[uint64]string{a.ID: "HYDYG6666----666666", b.ID: "HYDYG6666----777777"}
	for _, sku := range []*ent.ProductSku{a, b} {
		batch, err := cards.ImportConfirm(ctx, inventory.ImportInput{ProductID: p.ID, SkuID: sku.ID, Lines: []string{values[sku.ID]}})
		if err != nil || batch.Imported != 1 {
			t.Fatal("complete credential import failed", err)
		}
	}
	password := "synthetic-fetch-password"
	gen, _ := id.NewGenerator(31)
	uc := &ordermod.OrderUsecase{Data: d, Inv: cards, Gen: gen, Catalog: catalog.NewProductRepoImpl(d, nil), Outbox: data.NewOutboxWriter(d)}
	created, err := uc.CreateOrder(ctx, ordermod.CreateOrderInput{UserID: 3, QueryPassword: password, Items: []ordermod.OrderItemInput{{ProductID: p.ID, SkuID: a.ID, Quantity: 1}}})
	if err != nil {
		t.Fatal("actual checkout failed", err)
	}
	o := d.Client.Order.Query().Where(order.OrderNo(created.OrderNo)).OnlyX(ctx)
	it := d.Client.OrderItem.Query().Where(orderitem.OrderID(o.ID), orderitem.SkuID(a.ID)).OnlyX(ctx)
	reservedIDs := d.Client.Card.Query().Where(card.OrderID(o.ID), card.StatusEQ(card.StatusReserved)).IDsX(ctx)
	plain, err := cards.Contents(ctx, reservedIDs, p.ID, 0)
	if err != nil || len(plain) != 1 || plain[0] != values[a.ID] {
		t.Fatal("reserved card contents were truncated", err)
	}
	if err := data.Tx(ctx, d, func(txCtx context.Context) error { return uc.MarkPaid(txCtx, o.OrderNo) }); err != nil {
		t.Fatal("payment transition failed", err)
	}
	if err := deliveries.FulfillOrder(ctx, o.OrderNo); err != nil {
		t.Fatal(err)
	}
	service := NewStoreDeliveryService(deliveries)
	if _, err := service.FetchDelivery(ctx, &storefrontv1.FetchDeliveryRequest{OrderNo: o.OrderNo, QueryPassword: "wrong"}); err == nil {
		t.Fatal("wrong query password exposed a complete credential")
	}
	for i := 0; i < 2; i++ {
		result, err := service.FetchDelivery(ctx, &storefrontv1.FetchDeliveryRequest{OrderNo: o.OrderNo, QueryPassword: password})
		if err != nil || len(result.Items) != 1 || result.Items[0].Content != values[a.ID] || result.Items[0].Masked || result.Items[0].ItemId != it.ID || result.Items[0].SkuName != a.Name {
			t.Fatal("authorized repeat fetch did not return the complete selected-SKU card", err)
		}
	}
	admin := NewAdminFulfillmentService(deliveries, d)
	adminResult, err := admin.ListDeliveries(ctx, &adminv1.ListDeliveriesRequest{OrderNo: o.OrderNo})
	if err != nil || len(adminResult.Deliveries) != 1 || adminResult.Deliveries[0].Content != values[a.ID] {
		t.Fatal("admin delivery record did not contain the complete card", err)
	}
	other := d.Client.Card.Query().Where(card.ProductID(p.ID), card.SkuID(b.ID)).OnlyX(ctx)
	if other.Status != card.StatusAvailable || other.OrderID != 0 {
		t.Fatal("delivery consumed another SKU's stock")
	}
	exported, err := cards.ExportCards(ctx, p.ID, &b.ID)
	if err != nil || len(exported) != 1 || exported[0] != values[b.ID] {
		t.Fatal("remaining SKU export lost the complete card", err)
	}
}
