package inventory

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
)

func contentFixture(t *testing.T) (*AdminInventoryService, *data.Data, *ent.Product, *ent.Card, context.Context) {
	t.Helper()
	s, d, p, sku, _ := inventorySKUFixture(t)
	row := putInventoryCard(t, s, p, &sku.ID, "HYDYG6666----666666", card.StatusAvailable, 0)
	ctx := identity.WithClaims(context.Background(), &authn.Claims{Subject: 77})
	return s, d, p, row, ctx
}
func TestCardContentViewEditRoundTrip(t *testing.T) {
	s, d, p, row, ctx := contentFixture(t)
	view, e := s.ViewCardContent(ctx, &adminv1.ViewCardContentRequest{Id: row.ID})
	if e != nil || view.Content != "HYDYG6666----666666" || !view.CanEdit {
		t.Fatalf("view %+v %v", view, e)
	}
	listed, e := s.ListCards(ctx, &adminv1.ListCardsRequest{ProductId: p.ID})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(listed)
	if strings.Contains(string(raw), view.Content) {
		t.Fatal("list exposed plaintext")
	}
	if _, e = s.UpdateCardContent(ctx, &adminv1.UpdateCardContentRequest{Id: row.ID, Content: "new"}); errors.Reason(e) != "inventory.VIEW_REQUIRED" {
		t.Fatal("missing version accepted", e)
	}
	next := "NEW----serial----activation"
	updated, e := s.UpdateCardContent(ctx, &adminv1.UpdateCardContentRequest{Id: row.ID, Content: next, ExpectedVersion: &view.Version})
	if e != nil {
		t.Fatal(e)
	}
	got := d.Client.Card.GetX(ctx, row.ID)
	plain, e := s.repo.Cipher.Open(got.Content, p.ID, p.SubsiteID)
	if e != nil || plain != next || string(got.Content) == next || got.ContentHash != s.repo.Cipher.ContentHash(next) {
		t.Fatal("full encrypted content not saved", e)
	}
	if got.SkuID != row.SkuID || got.Status != row.Status || got.ImportID != row.ImportID || updated.Version != view.Version+1 {
		t.Fatal("edit changed identity or state")
	}
	if _, e = s.UpdateCardContent(ctx, &adminv1.UpdateCardContentRequest{Id: row.ID, Content: "stale", ExpectedVersion: &view.Version}); errors.Reason(e) != "inventory.CARD_CHANGED" {
		t.Fatal("stale edit accepted", e)
	}
	duplicate := putInventoryCard(t, s, p, nil, "duplicate", card.StatusDisabled, 0)
	if _, e = s.UpdateCardContent(ctx, &adminv1.UpdateCardContentRequest{Id: row.ID, Content: "duplicate", ExpectedVersion: &updated.Version}); errors.Reason(e) != "inventory.DUPLICATE_CONTENT" {
		t.Fatal("duplicate accepted", e)
	}
	if d.Client.Card.GetX(ctx, duplicate.ID).ContentHash != duplicate.ContentHash {
		t.Fatal("duplicate mutated")
	}
	logs := d.Client.SecurityAuditLog.Query().AllX(ctx)
	if len(logs) != 2 {
		t.Fatalf("wanted view/edit audits, got %d", len(logs))
	}
	for _, log := range logs {
		encoded, _ := json.Marshal(log.Metadata)
		if log.ActorID != 77 || strings.Contains(string(encoded), next) || strings.Contains(string(encoded), got.ContentHash) || strings.Contains(string(encoded), "666666") {
			t.Fatal("unsafe audit metadata")
		}
	}
	for _, value := range []string{"", "one\ntwo", "one\rtwo", "one\x00two", strings.Repeat("x", 65537)} {
		if _, e = s.UpdateCardContent(ctx, &adminv1.UpdateCardContentRequest{Id: row.ID, Content: value, ExpectedVersion: &updated.Version}); errors.Reason(e) != "inventory.INVALID_CONTENT" {
			t.Fatal("invalid card accepted", e)
		}
	}
}
func TestCardContentEditProtectedStatesAndTenant(t *testing.T) {
	for _, kind := range []string{"reserved", "used", "owner", "order", "locked", "used_at", "award", "product_lock", "archived"} {
		t.Run(kind, func(t *testing.T) {
			s, d, p, row, ctx := contentFixture(t)
			u := d.Client.Card.UpdateOneID(row.ID)
			switch kind {
			case "reserved":
				u.SetStatus(card.StatusReserved)
			case "used":
				u.SetStatus(card.StatusUsed)
			case "owner":
				u.SetOwnerID(19)
			case "order":
				u.SetOrderID(27)
			case "locked":
				u.SetLockedAt(time.Now())
			case "used_at":
				u.SetUsedAt(time.Now())
			case "award":
				d.Client.LotteryDraw.Create().SetActivityID(1).SetUserID(1).SetDrawNo("content-award").SetRequestKey("content-award").SetActivityName("award").SetStatus("delivered").SetRevision(1).SetCardID(row.ID).SetProductID(p.ID).SaveX(ctx)
			case "product_lock":
				d.Client.Product.UpdateOneID(p.ID).SetIsLocked(true).ExecX(ctx)
			case "archived":
				d.Client.Product.UpdateOneID(p.ID).SetStatus(-1).ExecX(ctx)
			}
			if kind != "award" && kind != "product_lock" && kind != "archived" {
				u.ExecX(ctx)
			}
			view, e := s.ViewCardContent(ctx, &adminv1.ViewCardContentRequest{Id: row.ID})
			if e != nil || view.CanEdit || view.EditBlockReason == "" {
				t.Fatalf("state not blocked %+v %v", view, e)
			}
			_, e = s.UpdateCardContent(ctx, &adminv1.UpdateCardContentRequest{Id: row.ID, Content: "must-not-write", ExpectedVersion: &view.Version})
			if e == nil {
				t.Fatal("protected card edited")
			}
			if d.Client.Card.GetX(ctx, row.ID).ContentHash != row.ContentHash {
				t.Fatal("protected content changed")
			}
		})
	}
	s, _, _, row, ctx := contentFixture(t)
	tenant := tenancy.WithContext(ctx, tenancy.Context{SubsiteID: 9})
	v := int32(0)
	if _, e := s.ViewCardContent(tenant, &adminv1.ViewCardContentRequest{Id: row.ID}); errors.Reason(e) != "inventory.CARD_NOT_FOUND" {
		t.Fatal("cross-tenant view", e)
	}
	if _, e := s.UpdateCardContent(tenant, &adminv1.UpdateCardContentRequest{Id: row.ID, Content: "cross", ExpectedVersion: &v}); errors.Reason(e) != "inventory.CARD_NOT_FOUND" {
		t.Fatal("cross-tenant edit", e)
	}
}
func TestCardContentAuditFailureDoesNotRevealOrCommit(t *testing.T) {
	s, d, _, row, ctx := contentFixture(t)
	d.Client.SecurityAuditLog.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, stderrors.New("audit unavailable") })
	})
	if out, e := s.ViewCardContent(ctx, &adminv1.ViewCardContentRequest{Id: row.ID}); out != nil || errors.Reason(e) != "inventory.AUDIT_FAILED" {
		t.Fatal("audit failure revealed card", out, e)
	}
	v := row.Version
	if out, e := s.UpdateCardContent(ctx, &adminv1.UpdateCardContentRequest{Id: row.ID, Content: "must-rollback", ExpectedVersion: &v}); out != nil || errors.Reason(e) != "inventory.AUDIT_FAILED" {
		t.Fatal("audit failure committed edit", out, e)
	}
	current := d.Client.Card.GetX(ctx, row.ID)
	if current.ContentHash != row.ContentHash || current.Version != row.Version {
		t.Fatal("audit failure did not roll back")
	}
}
func TestCardContentPermissionIsSeparateAndSensitive(t *testing.T) {
	directory := authz.BuildDirectory()
	code, public, ok := directory.PermissionForOp("zcard.api.admin.v1.AdminInventoryService/UpdateCardContent")
	if !ok || public || code != "card:edit_content" {
		t.Fatal("edit permission not enforced", code)
	}
	for _, p := range directory.All() {
		if p.Code == code {
			if !p.AdminOnly {
				t.Fatal("edit permission not sensitive")
			}
			return
		}
	}
	t.Fatal("missing edit permission")
}
