package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerRejectsWrongMethod(t *testing.T) {
	server := NewServer(&fakeBackend{})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/devices", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
}
