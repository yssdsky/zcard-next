package inventory

import (
	"context"
	"strings"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/securityauditlog"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
)

func (s *AdminInventoryService) cardContentEditBlock(ctx context.Context, row *ent.Card, p *ent.Product) (string, error) {
	if p.Status < 0 {
		return "商品已删除，不能编辑卡密", nil
	}
	if p.IsLocked {
		return "商品已锁定，请先解锁", nil
	}
	if row.Status != card.StatusAvailable && row.Status != card.StatusDisabled {
		return "预占或已售卡密不能编辑", nil
	}
	if row.OwnerID != 0 || row.OrderID != 0 || !row.LockedAt.IsZero() || !row.UsedAt.IsZero() {
		return "卡密已分配、锁定或使用，不能编辑", nil
	}
	refs, err := lotteryCardReferences(ctx, data.Client(ctx, s.data), []uint64{row.ID})
	if err != nil {
		return "", err
	}
	if refs[row.ID] {
		return "卡密已有发奖记录，不能编辑", nil
	}
	return "", nil
}

// ViewCardContent only decrypts after the explicit, permission-checked action.
// The revision is a card version, never a hash of its saleable content.
func (s *AdminInventoryService) ViewCardContent(ctx context.Context, req *adminv1.ViewCardContentRequest) (*adminv1.ViewCardContentReply, error) {
	c := data.Client(ctx, s.data)
	row, err := c.Card.Query().Where(card.ID(req.GetId()), card.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("inventory.CARD_NOT_FOUND", "卡密不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("inventory.GET_FAILED", "查询失败")
	}
	p, err := c.Product.Query().Where(product.ID(row.ProductID), product.SubsiteID(row.SubsiteID)).Only(ctx)
	if err != nil {
		return nil, errors.NotFound("inventory.PRODUCT_NOT_FOUND", "商品不存在")
	}
	reason, err := s.cardContentEditBlock(ctx, row, p)
	if err != nil {
		return nil, errors.InternalServer("inventory.GET_FAILED", "查询卡密状态失败")
	}
	if s.repo.Cipher == nil {
		return nil, errors.InternalServer("inventory.CIPHER_MISSING", "卡密密钥未配置")
	}
	plain, err := s.repo.Cipher.Open(row.Content, row.ProductID, row.SubsiteID)
	if err != nil {
		return nil, errors.InternalServer("inventory.DECRYPT_FAILED", "解密失败（密钥可能已轮换）")
	}
	if err = s.cardContentAudit(ctx, "card.view_content", row); err != nil {
		return nil, errors.InternalServer("inventory.AUDIT_FAILED", "记录查看日志失败")
	}
	return &adminv1.ViewCardContentReply{Id: row.ID, Content: plain, Version: row.Version, CanEdit: reason == "", EditBlockReason: reason}, nil
}

func (s *AdminInventoryService) cardContentAudit(ctx context.Context, action string, row *ent.Card) error {
	return data.Client(ctx, s.data).SecurityAuditLog.Create().SetActorType(securityauditlog.ActorTypeAdmin).SetNillableActorID(nilOr(actorID(ctx))).SetAction(action).SetMetadata(map[string]any{"card_id": row.ID, "product_id": row.ProductID, "subsite_id": row.SubsiteID}).Exec(ctx)
}

// UpdateCardContent accepts one complete credential. It never interprets --- as
// a separator and never changes product, SKU, ownership, or delivery status.
func (s *AdminInventoryService) UpdateCardContent(ctx context.Context, req *adminv1.UpdateCardContentRequest) (out *adminv1.UpdateCardContentReply, err error) {
	plain := strings.TrimSpace(req.GetContent())
	if req.GetId() == 0 || plain == "" || len(plain) > 65536 || strings.ContainsAny(plain, "\r\n\x00") {
		return nil, errors.BadRequest("inventory.INVALID_CONTENT", "请填写单张完整卡密，不可为空、换行或超过 64 KiB")
	}
	if req.ExpectedVersion == nil || req.GetExpectedVersion() < 0 {
		return nil, errors.BadRequest("inventory.VIEW_REQUIRED", "请先查看卡密后再编辑")
	}
	if s.repo.Cipher == nil {
		return nil, errors.InternalServer("inventory.CIPHER_MISSING", "卡密密钥未配置")
	}
	// Discover the immutable product ID without locking a card before its product.
	initial, e := data.Client(ctx, s.data).Card.Query().Where(card.ID(req.GetId()), card.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).Select(card.FieldID, card.FieldProductID).Only(ctx)
	if ent.IsNotFound(e) {
		return nil, errors.NotFound("inventory.CARD_NOT_FOUND", "卡密不存在")
	}
	if e != nil {
		return nil, errors.InternalServer("inventory.GET_FAILED", "读取卡密失败")
	}
	err = data.Tx(ctx, s.data, func(ctx context.Context) error {
		p, e := data.GuardProductWrite(ctx, s.data, initial.ProductID)
		if e != nil {
			return e
		}
		c := data.Client(ctx, s.data)
		q := c.Card.Query().Where(card.ID(req.GetId()), card.ProductID(p.ID), card.SubsiteID(p.SubsiteID))
		if s.data.Dialect.Capabilities().SupportsSkipLocked {
			q = q.ForUpdate()
		}
		row, e := q.Only(ctx)
		if ent.IsNotFound(e) {
			return errors.NotFound("inventory.CARD_NOT_FOUND", "卡密不存在")
		}
		if e != nil {
			return e
		}
		if row.Version != req.GetExpectedVersion() {
			return errors.Conflict("inventory.CARD_CHANGED", "卡密已变化，请关闭后重新查看")
		}
		reason, e := s.cardContentEditBlock(ctx, row, p)
		if e != nil {
			return e
		}
		if reason != "" {
			return errors.Conflict("inventory.CARD_NOT_EDITABLE", reason)
		}
		encrypted, e := s.repo.Cipher.Seal(plain, p.ID, p.SubsiteID)
		if e != nil {
			return errors.InternalServer("inventory.ENCRYPT_FAILED", "卡密加密失败")
		}
		update := c.Card.Update().Where(card.ID(row.ID), card.ProductID(p.ID), card.SubsiteID(p.SubsiteID), card.Version(row.Version), card.StatusIn(card.StatusAvailable, card.StatusDisabled), card.OwnerID(0), card.Or(card.OrderIDIsNil(), card.OrderID(0)), card.LockedAtIsNil(), card.UsedAtIsNil()).SetContent(encrypted).SetContentHash(s.repo.Cipher.ContentHash(plain)).AddVersion(1)
		if row.NumberHash != "" {
			update.SetNumberHash(s.repo.Cipher.ContentHash("number:" + plain))
		}
		n, e := update.Save(ctx)
		if ent.IsConstraintError(e) {
			return errors.Conflict("inventory.DUPLICATE_CONTENT", "同一商品已存在此卡密，未保存修改")
		}
		if e != nil {
			return e
		}
		if n != 1 {
			return errors.Conflict("inventory.CARD_CHANGED", "卡密状态已变化，请关闭后重新查看")
		}
		if e = s.cardContentAudit(ctx, "card.edit_content", row); e != nil {
			return errors.InternalServer("inventory.AUDIT_FAILED", "记录编辑日志失败，修改未保存")
		}
		out = &adminv1.UpdateCardContentReply{Id: row.ID, Version: row.Version + 1}
		return nil
	})
	if err != nil {
		if errors.Reason(err) == "inventory.AUDIT_FAILED" {
			return nil, err
		}
		return nil, inventoryTransportError(err, "inventory.UPDATE_FAILED", "更新卡密失败")
	}
	return out, nil
}
