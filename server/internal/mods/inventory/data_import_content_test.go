package inventory

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
)

func TestSKUImportPreservesCompleteCardLines(t *testing.T) {
	svc, d, p, a, b := inventorySKUFixture(t)
	ctx := context.Background()
	lines := []string{
		"HYDYG6666----666666",
		"HYDYG6666----777777",
		"account---123---activation",
		"literal---12.34---remark",
		"邮箱@example.test----密码----二次验证",
	}
	input := append(append([]string{}, lines...), lines[0])
	preview, err := svc.ImportPreview(ctx, &adminv1.ImportPreviewRequest{ProductId: p.ID, SkuId: a.ID, Lines: input, Dedup: true})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Total != int32(len(input)) || preview.DupInFile != 1 || preview.IsPremium || len(preview.Sample) != len(lines) {
		t.Fatalf("plain-text import misclassified credentials or deduplication: total=%d duplicates=%d premium=%t samples=%d", preview.Total, preview.DupInFile, preview.IsPremium, len(preview.Sample))
	}
	for i, sample := range preview.Sample {
		if sample != lines[i] {
			t.Fatal("preview changed a complete credential")
		}
	}
	batch, err := svc.ImportConfirm(ctx, &adminv1.ImportConfirmRequest{ProductId: p.ID, SkuId: a.ID, Lines: input, Dedup: true, ExpectedRevision: &preview.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Imported != int32(len(lines)) || batch.Skipped != 1 {
		t.Fatalf("import lost distinct credentials sharing a prefix: imported=%d skipped=%d", batch.Imported, batch.Skipped)
	}
	rows := d.Client.Card.Query().Where(card.ProductID(p.ID)).Order(ent.Asc(card.FieldID)).AllX(ctx)
	if len(rows) != len(lines) {
		t.Fatal("unexpected stored card count")
	}
	for i, row := range rows {
		plain, err := svc.repo.Cipher.Open(row.Content, p.ID, p.SubsiteID)
		if err != nil || plain != lines[i] || row.ContentHash != svc.repo.Cipher.ContentHash(lines[i]) || row.NumberHash != "" || row.SkuID != a.ID {
			t.Fatal("stored credential or SKU differs from the complete imported line")
		}
		if bytes.Contains(row.Content, []byte(lines[i])) {
			t.Fatal("stored card contains plaintext")
		}
		if _, err := svc.repo.Cipher.Open(row.Content, p.ID+1, p.SubsiteID); err == nil {
			t.Fatal("card ciphertext lost product AAD binding")
		}
	}
	exported, err := svc.repo.ExportCards(ctx, p.ID, &a.ID)
	if err != nil || len(exported) != len(lines) {
		t.Fatal("complete cards could not be exported from their SKU", err)
	}
	for i, line := range exported {
		if line != lines[i] {
			t.Fatal("export truncated a credential")
		}
	}
	preview, err = svc.ImportPreview(ctx, &adminv1.ImportPreviewRequest{ProductId: p.ID, SkuId: b.ID, Lines: lines})
	if err != nil || preview.DupInDb != int32(len(lines)) {
		t.Fatal("product-wide duplicate protection changed across SKUs", err)
	}
	other, err := svc.repo.ExportCards(ctx, p.ID, &b.ID)
	if err != nil || len(other) != 0 {
		t.Fatal("export leaked cards from a different SKU", err)
	}
}

func TestPremiumImportRequiresExplicitFormat(t *testing.T) {
	svc, d, p, a, _ := inventorySKUFixture(t)
	ctx := context.Background()
	lines := []string{"18888---99.00---legacy metadata", "18888---20.00---same number"}
	preview, err := svc.ImportPreview(ctx, &adminv1.ImportPreviewRequest{ProductId: p.ID, SkuId: a.ID, Lines: lines, ImportFormat: "premium"})
	if err != nil || !preview.IsPremium || preview.DupInFile != 1 || len(preview.Sample) != 1 || preview.Sample[0] != "18888" {
		t.Fatal("explicit premium preview did not match its delivered number", err)
	}
	batch, err := svc.ImportConfirm(ctx, &adminv1.ImportConfirmRequest{ProductId: p.ID, SkuId: a.ID, Lines: lines, ImportFormat: "premium"})
	if err != nil || batch.Imported != 1 || batch.Skipped != 1 {
		t.Fatal("explicit legacy premium import failed", err)
	}
	row := d.Client.Card.Query().OnlyX(ctx)
	plain, err := svc.repo.Cipher.Open(row.Content, p.ID, p.SubsiteID)
	if err != nil || plain != "18888" || row.NumberHash != svc.repo.Cipher.ContentHash("number:18888") || row.Price != 0 || row.Note != "" {
		t.Fatal("legacy premium number selection or existing metadata behavior changed")
	}
	before := d.Client.CardImport.Query().CountX(ctx)
	for _, format := range []string{"auto", "csv"} {
		_, err = svc.ImportPreview(ctx, &adminv1.ImportPreviewRequest{ProductId: p.ID, SkuId: a.ID, Lines: lines, ImportFormat: format})
		requireInventoryReason(t, err, "inventory.IMPORT_FORMAT_INVALID")
		_, err = svc.ImportConfirm(ctx, &adminv1.ImportConfirmRequest{ProductId: p.ID, SkuId: a.ID, Lines: lines, ImportFormat: format})
		requireInventoryReason(t, err, "inventory.IMPORT_FORMAT_INVALID")
	}
	if d.Client.CardImport.Query().CountX(ctx) != before {
		t.Fatal("invalid format created a batch")
	}
}
