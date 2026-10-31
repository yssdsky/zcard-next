package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
)

func TestInstallAndOrderCredentialsAreRedactedAtAuditBoundaries(t *testing.T) {
	const payload = `{"admin_username":"owner","admin_password":"synthetic-install-secret","adminPassword":"synthetic-camel-secret","query_password":"synthetic-query-secret","order_access_token":"synthetic-token-secret"}`
	r := httptest.NewRequest("POST", "/api/v1/admin/install", strings.NewReader(payload))
	metadata := ReadBodyJSON(r)
	raw, err := json.Marshal(metadata)
	if err != nil || strings.Contains(string(raw), "synthetic-") || metadata["admin_username"] != "owner" {
		t.Fatal("installation audit kept credentials or removed non-secret metadata", err)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || string(body) != payload {
		t.Fatal("audit filter changed the installation request")
	}

	repo, d := newAuditRepo(t)
	ctx := context.Background()
	var original map[string]any
	if err := json.Unmarshal([]byte(payload), &original); err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteOpLog(ctx, OpLogInput{OperatorType: "admin", Action: "POST", Route: "/api/v1/admin/install", Before: original, After: original}); err != nil {
		t.Fatal(err)
	}
	stored := d.Client.AuditLog.Query().OnlyX(ctx)
	encoded, _ := json.Marshal([]any{stored.Before, stored.After})
	if strings.Contains(string(encoded), "synthetic-") {
		t.Fatal("direct audit writes persisted credentials")
	}
	// Older installations already have raw fields in the database. Reading their
	// audit records must not expose them through the current administration API.
	d.Client.AuditLog.Create().SetOperatorType("admin").SetAction("POST").SetRoute("/api/v1/admin/install").SetBefore(original).SetAfter(original).SaveX(ctx)
	reply, err := NewAdminAuditService(repo, nil, nil).ListOpLogs(ctx, &adminv1.ListOpLogsRequest{})
	encoded, _ = json.Marshal(reply)
	if err != nil || reply.Total != 2 || strings.Contains(string(encoded), "synthetic-") || !strings.Contains(string(encoded), "owner") {
		t.Fatal("historical audit credentials leaked through the API", err)
	}
}
