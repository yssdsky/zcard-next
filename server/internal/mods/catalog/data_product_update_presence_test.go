package catalog

import (
	"context"
	"fmt"
	"testing"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/go-kratos/kratos/v3/encoding"
	_ "github.com/go-kratos/kratos/v3/encoding/json"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func decodeProductUpdate(t *testing.T, id uint64, body string) *adminv1.UpdateProductRequest {
	t.Helper()
	req := &adminv1.UpdateProductRequest{}
	if err := encoding.GetCodec("json").Unmarshal([]byte(body), req); err != nil {
		t.Fatal(err)
	}
	req.Id = id
	return req
}

func TestProductQuickPriceEditPreservesPresentationAndRecommendation(t *testing.T) {
	d, svc := newStatsEnv(t)
	ctx := context.Background()
	cat := d.Client.Category.Create().SetName("existing category").SaveX(ctx)
	p := d.Client.Product.Create().SetName("recommended product").SetSlug("recommended-product").
		SetCategoryID(cat.ID).SetPrice(100).SetFactoryPrice(50).SetPointsRequired(30).SetStatus(1).
		SetIsRecommend(true).SetCover("/uploads/cover.png").SetImages([]string{"/uploads/gallery.png"}).
		SetDescription("<p>Keep this description</p>").SaveX(ctx)
	// The list's price and cost editors send a partial request, without presentation fields.
	for _, edit := range []struct {
		body        string
		price, cost int64
	}{
		{`{"price_cents":200,"factory_price_cents":50,"points_required":30,"status":1,"sort":0,"stock_visible":true,"delivery_mode":"status"}`, 200, 50},
		{`{"factory_price_cents":70,"points_required":30,"status":1,"sort":0,"stock_visible":true,"delivery_mode":"status"}`, 200, 70},
	} {
		req := decodeProductUpdate(t, p.ID, edit.body)
		if _, err := svc.UpdateProduct(ctx, req); err != nil {
			t.Fatal(err)
		}
		got := d.Client.Product.GetX(ctx, p.ID)
		if got.Price != edit.price || got.FactoryPrice != edit.cost || got.PointsRequired != 30 || got.CategoryID != cat.ID || !got.IsRecommend || got.Cover != p.Cover || len(got.Images) != 1 || got.Images[0] != p.Images[0] || got.Description != p.Description {
			t.Fatalf("partial edit changed unrelated fields: %+v", got)
		}
		items, total, err := svc.repo.ListVisible(ctx, port.VisibleFilter{RecommendOnly: true, Page: 1, PageSize: 10})
		if err != nil || total != 1 || len(items) != 1 || items[0].ID != p.ID {
			t.Fatalf("recommendation disappeared after price edit: %+v total=%d err=%v", items, total, err)
		}
	}
	for _, recommend := range []bool{false, true} {
		req := decodeProductUpdate(t, p.ID, fmt.Sprintf(`{"factory_price_cents":70,"points_required":30,"status":1,"sort":0,"stock_visible":true,"delivery_mode":"status","is_recommend":%t}`, recommend))
		if _, err := svc.UpdateProduct(ctx, req); err != nil {
			t.Fatal(err)
		}
		got := d.Client.Product.GetX(ctx, p.ID)
		if got.IsRecommend != recommend || got.Description != p.Description || got.FactoryPrice != 70 || got.PointsRequired != 30 {
			t.Fatalf("explicit recommendation update failed or erased description: %+v", got)
		}
	}
}

func TestProductMediaUpdatePresenceAndReferences(t *testing.T) {
	d, svc := newStatsEnv(t)
	ctx := context.Background()
	cat := d.Client.Category.Create().SetName("original category").SaveX(ctx)
	cover := batchImage(t, d, "cover-presence")
	gallery := batchImage(t, d, "gallery-presence")
	p := d.Client.Product.Create().SetName("initially without images").SetSlug("without-images").
		SetCategoryID(cat.ID).SetPrice(100).SetStatus(1).SaveX(ctx)
	update := func(body string) *ent.Product {
		t.Helper()
		if _, err := svc.UpdateProduct(ctx, decodeProductUpdate(t, p.ID, body)); err != nil {
			t.Fatal(err)
		}
		got := d.Client.Product.GetX(ctx, p.ID)
		if got.CategoryID != cat.ID {
			t.Fatal("media update changed the saved category")
		}
		return got
	}
	got := update(fmt.Sprintf(`{"price_cents":100,"status":1,"cover":%q,"images":[%q],"description":"<p>Added later</p>"}`, "/uploads/"+cover.Path, "/uploads/"+gallery.Path))
	if got.Cover == "" || len(got.Images) != 1 || got.Description != "<p>Added later</p>" || d.Client.Media.GetX(ctx, cover.ID).RefCount != 1 || d.Client.Media.GetX(ctx, gallery.ID).RefCount != 1 {
		t.Fatalf("adding media to existing product failed: %+v", got)
	}
	got = update(`{"price_cents":200,"status":1}`)
	if got.Cover == "" || len(got.Images) != 1 || got.Description != "<p>Added later</p>" {
		t.Fatalf("omitted media fields did not preserve content: %+v", got)
	}
	got = update(`{"price_cents":200,"status":1,"cover":"","description":""}`)
	if got.Cover != "" || got.Description != "" || len(got.Images) != 1 || d.Client.Media.GetX(ctx, cover.ID).RefCount != 0 || d.Client.Media.GetX(ctx, gallery.ID).RefCount != 1 {
		t.Fatalf("explicit empty cover/description did not clear only those fields: %+v", got)
	}
	got = update(`{"price_cents":200,"status":1,"images":[]}`)
	if len(got.Images) != 1 {
		t.Fatal("legacy empty images without images_set changed the gallery")
	}
	got = update(`{"price_cents":200,"status":1,"images":[],"images_set":true}`)
	if len(got.Images) != 0 || d.Client.Media.GetX(ctx, gallery.ID).RefCount != 0 {
		t.Fatalf("explicit empty gallery did not clear images/references: %+v", got)
	}
}

func TestProductPresentationUpdateProtectsManualUpstreamContent(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprintf("clear=%t", clear), func(t *testing.T) {
			d, svc := newStatsEnv(t)
			ctx := context.Background()
			p := d.Client.Product.Create().SetName("upstream product").SetSlug("upstream-presentation").
				SetPrice(100).SetStatus(1).SetUpstreamSourceID(7).SetUpstreamProductCode("remote").
				SetCover("https://supplier.example/original.png").SetDescription("<p>Original upstream description</p>").SaveX(ctx)
			// Saving the same presentation with a price change must not stop future sync.
			body := fmt.Sprintf(`{"price_cents":100,"status":1,"cover":%q,"description":%q}`, p.Cover, p.Description)
			if _, err := svc.UpdateProduct(ctx, decodeProductUpdate(t, p.ID, body)); err != nil {
				t.Fatal(err)
			}
			got := d.Client.Product.GetX(ctx, p.ID)
			if got.CoverProtected || got.DescriptionProtected {
				t.Fatal("unchanged upstream content became protected")
			}
			cover, description := "https://shop.example/manual.png", "<p>Manual description</p>"
			if clear {
				cover, description = "", ""
			}
			body = fmt.Sprintf(`{"price_cents":100,"status":1,"cover":%q,"description":%q}`, cover, description)
			if _, err := svc.UpdateProduct(ctx, decodeProductUpdate(t, p.ID, body)); err != nil {
				t.Fatal(err)
			}
			got = d.Client.Product.GetX(ctx, p.ID)
			if !got.CoverProtected || !got.DescriptionProtected {
				t.Fatal("manual upstream edits were not protected")
			}
			_, _, err := svc.repo.UpsertUpstreamProduct(ctx, port.UpstreamProductInput{ConnectionID: 7, UpstreamProductCode: "remote", Name: "upstream product", Price: 150, Status: 1, Cover: "https://supplier.example/new.png", Description: "replacement", DescriptionSet: true})
			if err != nil {
				t.Fatal(err)
			}
			got = d.Client.Product.GetX(ctx, p.ID)
			if got.Cover != cover || got.Description != description {
				t.Fatalf("upstream sync overwrote manual presentation: %+v", got)
			}
		})
	}
}

func TestProductUpdatePresenceSurvivesJSONAndProtobuf(t *testing.T) {
	for _, tc := range []struct {
		body    string
		present bool
	}{
		{`{}`, false},
		{`{"is_recommend":null,"cover":null,"description":null}`, false},
		{`{"is_recommend":false,"cover":"","description":"","images_set":true}`, true},
	} {
		req := decodeProductUpdate(t, 1, tc.body)
		wire, err := proto.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		roundtrip := &adminv1.UpdateProductRequest{}
		if err := proto.Unmarshal(wire, roundtrip); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"is_recommend", "cover", "description", "images_set"} {
			field := roundtrip.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
			if field == nil || roundtrip.ProtoReflect().Has(field) != tc.present {
				t.Fatalf("field %s lost presence for %s", name, tc.body)
			}
		}
	}
}

func TestSocialProductPresentationOnlyUpdate(t *testing.T) {
	d, svc := newStatsEnv(t)
	ctx := context.Background()
	p := d.Client.Product.Create().SetName("social service").SetSlug("social-presentation").
		SetDeliveryKind("social_service").SetPrice(0).SetStatus(1).SaveX(ctx)
	req := decodeProductUpdate(t, p.ID, `{"name":"Social with cover","status":1,"cover":"https://shop.example/social.png","description":"<p>Social description</p>","images":["https://shop.example/gallery.png"],"images_set":true,"is_recommend":true}`)
	if _, err := svc.UpdateProduct(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := d.Client.Product.GetX(ctx, p.ID)
	if got.Name != "Social with cover" || got.Cover != req.GetCover() || got.Description != req.GetDescription() || len(got.Images) != 1 || !got.IsRecommend || got.DeliveryKind != "social_service" || got.Price != 0 {
		t.Fatalf("social presentation update failed: %+v", got)
	}
}
