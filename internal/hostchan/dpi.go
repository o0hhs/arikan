package hostchan

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"mikan/internal/nodeapi"
)

// The DPI bypass on a node's own server: the node's container cannot run nfqws (no
// NET_ADMIN), so it writes a request and a systemd path unit on the host starts the service
// (installer/src/dpi.rs). The host writes what happened into status.json; the node hands
// that to the panel. The files:
//
//	dpi/request      {"id", "at", "on": true}: start nfqws, false stops it
//	dpi/status.json  {"on", "error", "at"}: written by the host after the request
const dpiDir = "dpi"

// maxDPI is far above what the status holds: a version, a flag and a short error.
const maxDPI = 8 << 10

// RequestDPI asks the host to start (on) or stop the bypass. The host takes the request a
// moment later; the node reads the result with DPI.
func (c *Channel) RequestDPI(on bool) error {
	if c == nil || c.dir == "" {
		return ErrUnavailable
	}
	raw, err := json.Marshal(struct {
		ID string `json:"id"`
		At string `json:"at"`
		On bool   `json:"on"`
	}{newID(), c.now().UTC().Format(time.RFC3339), on})
	if err != nil {
		return err
	}
	return c.put(dpiDir, raw)
}

// DPI reads the host's last answer; a host that never answered reads as off with no error
// (the panel can tell by At).
func (c *Channel) DPI() (nodeapi.DPIStatus, error) {
	var s nodeapi.DPIStatus
	if c == nil || c.dir == "" {
		return s, ErrUnavailable
	}
	path := filepath.Join(c.dir, dpiDir, "status.json")
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxDPI {
		return s, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return s, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxDPI+1))
	if err != nil || len(raw) > maxDPI {
		return s, err
	}
	if json.Unmarshal(raw, &s) != nil {
		return nodeapi.DPIStatus{}, nil
	}
	return s, nil
}
