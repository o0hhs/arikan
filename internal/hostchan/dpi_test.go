package hostchan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The node writes one small request and reads the host's answer: the files the host unit
// watches and writes (installer/src/dpi.rs).
func TestDPIRequestAndStatus(t *testing.T) {
	dir := t.TempDir()
	c := New(dir)
	if err := c.RequestDPI(true); err != nil {
		t.Fatalf("request: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dpi", "request"))
	if err != nil {
		t.Fatalf("request file: %v", err)
	}
	var req struct {
		ID string `json:"id"`
		On bool   `json:"on"`
	}
	if json.Unmarshal(raw, &req) != nil || !req.On || req.ID == "" {
		t.Fatalf("request is %q", raw)
	}
	// Until the host answers the status is off and says nothing.
	if st, err := c.DPI(); err != nil || st.On || st.At != 0 {
		t.Fatalf("status before the host: %+v, %v", st, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dpi", "status.json"), []byte(`{"on":true,"at":1700000000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := c.DPI()
	if err != nil || !st.On || st.At != 1700000000 {
		t.Fatalf("status: %+v, %v", st, err)
	}
}
