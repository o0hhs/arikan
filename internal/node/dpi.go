package node

import (
	"encoding/json"
	"errors"
	"net/http"

	"mikan/internal/hostchan"
	"mikan/internal/nodeapi"
)

// The DPI bypass of a node's own server: YouTube and Discord leave with the node's own
// address while nfqws (zapret) gets the TLS handshake past the ISP's filter. The node's
// container cannot run it, so it writes a request for its host (internal/hostchan dpi.go)
// and reports the host's answer; the panel toggles it. The host unit starts the service
// the first time; that install may take minutes, so the POST answers at once and the
// panel polls GET.
func dpiStatusHandler(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := e.hostChan().DPI()
		switch {
		case errors.Is(err, hostchan.ErrUnavailable):
			writeJSON(w, http.StatusConflict, nodeapi.Error{Code: nodeapi.CodeHostUnavailable, Message: "this node has no directory its host watches"})
		case err != nil:
			e.log.Error("host dpi status", "err", err)
			writeJSON(w, http.StatusInternalServerError, nodeapi.Error{Code: "dpi_failed", Message: "the status could not be read"})
		default:
			writeJSON(w, http.StatusOK, st)
		}
	}
}

func dpiHandler(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			On *bool `json:"on"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12))
		if err := dec.Decode(&in); err != nil || in.On == nil {
			writeJSON(w, http.StatusBadRequest, nodeapi.Error{Code: "bad_request", Message: `the body is {"on": true} or {"on": false}`})
			return
		}
		if err := e.hostChan().RequestDPI(*in.On); err != nil {
			if errors.Is(err, hostchan.ErrUnavailable) {
				writeJSON(w, http.StatusConflict, nodeapi.Error{Code: nodeapi.CodeHostUnavailable, Message: "this node has no directory its host watches"})
				return
			}
			e.log.Error("host dpi", "err", err)
			writeJSON(w, http.StatusInternalServerError, nodeapi.Error{Code: "dpi_failed", Message: "the request could not be written"})
			return
		}
		e.log.Info("DPI asked for by the panel", "on", *in.On)
		// The host answers after it acts; this is the state before it does, which the panel
		// polls until it changes.
		st, _ := e.hostChan().DPI()
		writeJSON(w, http.StatusAccepted, st)
	}
}
