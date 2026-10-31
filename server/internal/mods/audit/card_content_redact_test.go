package audit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCardEditAuditDropsContentAndKeepsVersion(t *testing.T) {
	body := `{"id":7,"expected_version":2,"content":"NEW-SECRET----ACTIVATION","previous_content":"OLD-SECRET","contentHash":"HASH-SECRET","expectedVersion":"ALIAS-SECRET"}`
	r := httptest.NewRequest(http.MethodPut, "/api/v1/admin/inventory/cards/7/content", strings.NewReader(body))
	metadata := ReadBodyJSON(r)
	raw, _ := json.Marshal(metadata)
	if strings.Contains(string(raw), "SECRET") || metadata["id"] != float64(7) || metadata["expected_version"] != float64(2) {
		t.Fatal("unsafe card edit audit", string(raw))
	}
	restored, _ := io.ReadAll(r.Body)
	if string(restored) != body {
		t.Fatal("redaction changed handler input")
	}
}
