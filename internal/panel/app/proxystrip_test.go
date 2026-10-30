package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mikan/internal/panel/server"
)

// A connection from the node's router names the client in the PROXY header, not in the
// request: the headers a client sends must not choose the address the device limit sees.
func TestTheNodeProxyDoesNotTrustForwardedFor(t *testing.T) {
	var got string
	h := stripForwarded(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = server.ClientIP(r.Header, "203.0.113.9:1234", true)
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "https://sub.example/abc", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req.Header.Set("X-Real-IP", "198.51.100.8")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "203.0.113.9" {
		t.Fatalf("the client is %q, want the PROXY peer", got)
	}
}
