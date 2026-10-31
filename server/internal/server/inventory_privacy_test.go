package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCardContentResponsesNeverCache(t *testing.T) {
	h := privateOrderResponseFilter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/admin/inventory/cards/1/content", nil))
		if w.Header().Get("Cache-Control") != "no-store, private" {
			t.Fatal("card credentials may be cached", method)
		}
	}
}
