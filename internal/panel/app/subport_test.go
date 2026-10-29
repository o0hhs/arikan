package app

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/server"
	"mikan/internal/panel/settings"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func get(port int, path string) (int, string, error) {
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// The subscription port serves the subscription path alone, moves without a restart and
// changes nothing when the new port is taken.
func TestSubPortListener(t *testing.T) {
	srv := server.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "admin") }),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "sub "+r.URL.Path) }))
	srv.SetPaths(settings.Paths{Admin: "adm1n", Sub: "s0b"})
	sp := NewSubPort("127.0.0.1", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sp.SetHandler(srv.SubOnly())
	t.Cleanup(sp.Close)

	a := freePort(t)
	if err := sp.Set(a); err != nil {
		t.Fatal(err)
	}
	if code, body, err := get(a, "/s0b/tok"); err != nil || code != http.StatusOK || body != "sub /tok" {
		t.Fatalf("subscription: %d %q %v", code, body, err)
	}
	if code, _, err := get(a, "/adm1n/"); err != nil || code != http.StatusNotFound {
		t.Fatalf("the admin panel on the subscription port: %d %v", code, err)
	}

	// A taken port: refused, and the old one keeps serving.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if err := sp.Set(busy.Addr().(*net.TCPAddr).Port); !errors.Is(err, ErrSubPortBusy) {
		t.Fatalf("taken port: %v", err)
	}
	if code, _, err := get(a, "/s0b/tok"); err != nil || code != http.StatusOK {
		t.Fatalf("the old port after a refused move: %d %v", code, err)
	}

	b := freePort(t)
	if err := sp.Set(b); err != nil {
		t.Fatal(err)
	}
	if code, _, err := get(b, "/s0b/tok"); err != nil || code != http.StatusOK {
		t.Fatalf("the new port: %d %v", code, err)
	}
	if _, _, err := get(a, "/s0b/tok"); err == nil {
		t.Fatal("the old port still listens after the move")
	}
	if err := sp.Set(0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := get(b, "/s0b/tok"); err == nil {
		t.Fatal("the port still listens after it was removed")
	}

	// At start a taken port is reported, not fatal.
	sp.Start(busy.Addr().(*net.TCPAddr).Port)
	if sp.Error() != "sub_port_busy" {
		t.Fatalf("start on a taken port: %q", sp.Error())
	}
}

// The subscription port over the admin API: refused where it would clash, links move to
// it, and the panel's own node cannot put a TCP inbound there afterwards.
func TestSubPortOverHTTP(t *testing.T) {
	opened := []int{}
	h := newHarness(t, func(o *Options) {
		o.SubPort = func(port int) error {
			if port == 9443 {
				return ErrSubPortBusy
			}
			opened = append(opened, port)
			return nil
		}
		o.SubPortError = func() string { return "" }
	})
	if err := domain.Seed(t.Context(), h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	// The seeded inbounds hold 443 and 8443 over TCP on the panel's own node. 443 is also
	// the shared port, and a link by address has no name for the router to pick: it needs
	// a domain before anything else.
	panelPort := 21355
	if err := settings.Set(t.Context(), settings.New(h.st.Q), settings.KeyPanelPort, panelPort); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(t.Context(), settings.New(h.st.Q), settings.KeyPublicHost, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	for port, code := range map[int]string{22: "sub_port_reserved", 80: "sub_port_reserved", panelPort: "sub_port_panel", 443: "sub_port_443_domain", 8443: "sub_port_inbound", 9443: "sub_port_busy"} {
		resp, body := h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_port": port}, csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), code) {
			t.Errorf("port %d: %d %s", port, resp.StatusCode, body)
		}
	}
	if len(opened) != 0 {
		t.Fatalf("ports opened for refused requests: %v", opened)
	}

	resp, body := h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_port": 2053}, csrf)
	var v struct {
		SubPort    int    `json:"sub_port"`
		SubBaseURL string `json:"sub_base_url"`
		AdminURL   string `json:"admin_url"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.SubPort != 2053 ||
		!strings.HasPrefix(v.SubBaseURL, "https://203.0.113.10:2053/") || !strings.HasPrefix(v.AdminURL, "https://203.0.113.10:21355/") {
		t.Fatalf("sub port: %d %s", resp.StatusCode, body)
	}
	if len(opened) != 1 || opened[0] != 2053 {
		t.Fatalf("opened: %v", opened)
	}

	// A TCP inbound on the panel's own node cannot take it; UDP can.
	if resp, body := h.do(http.MethodPost, api+"/inbounds", map[string]any{"preset": "vless_reality_vision", "port": "2053"}, csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "port_sub") {
		t.Fatalf("tcp inbound on the subscription port: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPost, api+"/inbounds", map[string]any{"preset": "hysteria2", "port": "2053"}, csrf); resp.StatusCode >= 300 {
		t.Fatalf("udp inbound on the subscription port: %d %s", resp.StatusCode, body)
	}

	// 0 takes it away: links go back to the panel's port.
	resp, body = h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_port": 0}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.SubPort != 0 || !strings.HasPrefix(v.SubBaseURL, "https://203.0.113.10:21355/") {
		t.Fatalf("sub port off: %d %s", resp.StatusCode, body)
	}
}

// A subscription port of 443 opens no listener of its own: 443 is the node's shared port,
// the SNI router holds it and hands the subscription domain to the panel's loopback
// listener. Nothing is reported busy, the old port closes, and a real port opens again.
func TestSubPort443(t *testing.T) {
	srv := server.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "sub "+r.URL.Path) }))
	srv.SetPaths(settings.Paths{Admin: "adm1n", Sub: "s0b"})
	sp := NewSubPort("127.0.0.1", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sp.SetHandler(srv.SubOnly())
	t.Cleanup(sp.Close)

	a := freePort(t)
	if err := sp.Set(a); err != nil {
		t.Fatal(err)
	}
	if err := sp.Set(443); err != nil || sp.Error() != "" {
		t.Fatalf("443 taken for a port to open: %v %q", err, sp.Error())
	}
	if _, _, err := get(a, "/s0b/tok"); err == nil {
		t.Fatal("the old port still listens after the move to 443")
	}
	b := freePort(t)
	if err := sp.Set(b); err != nil {
		t.Fatal(err)
	}
	if code, body, err := get(b, "/s0b/tok"); err != nil || code != http.StatusOK || body != "sub /tok" {
		t.Fatalf("back on its own port: %d %q %v", code, body, err)
	}
	// At start a saved 443 is no error either: it is the router's port, not a busy one.
	sp.Close()
	sp.Start(443)
	if sp.Error() != "" {
		t.Fatalf("start on 443: %q", sp.Error())
	}
}

// A subscription on 443 over the admin API: it needs a name (the router picks the panel out
// of the ClientHello), takes no listener, and the links carry no port.
func TestSubPort443OverHTTP(t *testing.T) {
	opened := []int{}
	h := newHarness(t, func(o *Options) {
		o.SubPort = func(port int) error {
			opened = append(opened, port)
			return nil
		}
		o.SubPortError = func() string { return "" }
	})
	if err := domain.Seed(t.Context(), h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	set := settings.New(h.st.Q)
	if err := settings.Set(t.Context(), set, settings.KeyPanelPort, 21355); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(t.Context(), set, settings.KeyDomain, "panel.example.com"); err != nil {
		t.Fatal(err)
	}
	// The seeded 443 inbound is off: 443 belongs to the node's router then.
	if _, err := h.st.DB.ExecContext(t.Context(), "UPDATE inbounds SET enabled = 0 WHERE port = '443'"); err != nil {
		t.Fatal(err)
	}
	resp, body := h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_port": 443}, csrf)
	var v struct {
		SubPort    int    `json:"sub_port"`
		SubBaseURL string `json:"sub_base_url"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.SubPort != 443 {
		t.Fatalf("sub port 443: %d %s", resp.StatusCode, body)
	}
	if want := "https://panel.example.com/" + subPath + "/"; v.SubBaseURL != want {
		t.Fatalf("the link on 443: %q, want %q", v.SubBaseURL, want)
	}
	if len(opened) != 1 || opened[0] != 443 {
		t.Fatalf("opened: %v", opened)
	}
	// Back to the panel's port: the link carries it again.
	resp, body = h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_port": 0}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || !strings.HasPrefix(v.SubBaseURL, "https://panel.example.com:21355/") {
		t.Fatalf("sub port off: %d %s", resp.StatusCode, body)
	}
}
