package domain

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"mikan/internal/nodetls"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// A copy is the same template with its keys on a port of its own: on the node it came
// from or another one, with the camouflage it is given.
func TestCopy(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, s := inbounds(t, &now, nil)
	ctx := context.Background()
	pool, err := st.Q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	src := find(t, s, 1, "vless-xhttp")
	// Give the source everything a copy might inherit.
	if _, next, err := s.Update(ctx, src.ID, InboundPatch{Port: ptr("2443"), PoolID: &pool.ID, Listen: ptr("127.0.0.1"), AutoSNI: ptr(false), DisplayName: ptr("Нидерланды")}); err != nil {
		t.Fatal(err)
	} else {
		src = next
	}

	cp, err := s.Copy(ctx, src.ID, CopyInbound{Port: "2543"})
	if err != nil {
		t.Fatal(err)
	}
	if cp.NodeID != 1 || cp.Preset != src.Preset || cp.Port != "2543" || cp.Enabled != 1 || cp.UpdatedAt != now.Unix() {
		t.Fatalf("copy: %+v", cp)
	}
	// Upstream's copy carries keys of its own: the template's shape stays, the secrets change.
	newTpl, err := proto.Parse(cp.Config)
	if err != nil {
		t.Fatal(err)
	}
	oldTpl, _ := proto.Parse(src.Config)
	if newTpl["type"] != oldTpl["type"] {
		t.Fatalf("the copy changed protocol: %v", newTpl["type"])
	}
	if cp.Name != "vless-xhttp-2" {
		t.Fatalf("name %q", cp.Name)
	}
	// What placement is: the same switches and pool; upstream's copy finds its own SNI and
	// leaves the listen address to its port.
	if cp.DisplayName != "" || cp.Listen != "" || cp.AutoPort != 1 || cp.AutoSni != 1 || cp.PoolID.Int64 != pool.ID {
		t.Fatalf("inherited: %+v", cp)
	}
	// A copy of a copy counts from its parent's name, as upstream names them.
	third, err := s.Copy(ctx, cp.ID, CopyInbound{Port: "2643"})
	if err != nil || third.Name != "vless-xhttp-2-2" {
		t.Fatalf("third: %+v %v", third, err)
	}

	// A port something else holds is refused: the source moved off 443, but vless-vision
	// holds 8443/tcp.
	var busy *PortInUseError
	if _, err := s.Copy(ctx, cp.ID, CopyInbound{Port: "8443"}); !errors.As(err, &busy) || busy.Name != "vless-vision" {
		t.Fatalf("tcp 8443 is taken: %v", err)
	}

	// Another node: its own names and ports, the template as it is.
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	n, _, err := AddNode(ctx, st, panel, NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, now)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := s.Copy(ctx, cp.ID, CopyInbound{NodeID: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	if remote.NodeID != n.ID || remote.Port != "2543" || remote.Name != "vless-xhttp-2" || remote.Listen != "" || remote.Outbound != "direct" || remote.ExitNodeID.Valid {
		t.Fatalf("remote copy: %+v", remote)
	}
	if _, err := s.Copy(ctx, cp.ID, CopyInbound{NodeID: n.ID, Port: "40000"}); !errors.As(err, &busy) || busy.Kind != PortNodeAPI {
		t.Fatalf("node B's API port: %v", err)
	}
}

// A copy may go under another site: the keys stay, the source stays as it was.
func TestCopyTarget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, s := inbounds(t, &now, nil)
	ctx := context.Background()
	src := find(t, s, 1, "vless-xhttp")
	cp, err := s.Copy(ctx, src.ID, CopyInbound{Port: "2543", Dest: " 203.0.113.20:443 ", ServerName: "www.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := proto.Parse(cp.Config)
	dest, names := presets.Dest(tpl)
	if dest != "203.0.113.20:443" || len(names) != 1 || names[0] != "www.example.org" {
		t.Fatalf("retargeted: %s %v", dest, names)
	}
	// The copy's REALITY key is its own now; the retarget kept the rest of the camouflage.
	if tpl["reality-config"].(map[string]any)["private-key"] == nil {
		t.Fatal("the copy lost its REALITY key")
	}
	if after := find(t, s, 1, "vless-xhttp"); after.Config != src.Config {
		t.Fatal("the copy changed its source")
	}

	// The target's own rules apply to the copy, as they do to any edit.
	var edit *EditError
	for _, c := range []struct {
		src  string
		port string
		dest string
		sni  string
		code string
	}{
		{"vless-vision", "2544", "203.0.113.20:443", "", "reality_sni"},
		{"vless-vision", "2544", "nope", "", "dest_format"},
		{"hysteria2", "2544", "www.example.org:443", "", "dest_no_reality"},
	} {
		in := find(t, s, 1, c.src)
		if _, err := s.Copy(ctx, in.ID, CopyInbound{Port: c.port, Dest: c.dest, ServerName: c.sni}); !errors.As(err, &edit) || edit.Err.Code != c.code {
			t.Errorf("%s dest %q sni %q: %v, want %s", c.src, c.dest, c.sni, err, c.code)
		}
	}
}

func TestCopyRefused(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, s := inbounds(t, &now, nil)
	ctx := context.Background()
	src := find(t, s, 1, "vless-xhttp")
	for _, c := range []struct {
		src  int64
		in   CopyInbound
		want error
	}{
		{999, CopyInbound{}, ErrUnknownInbound},
		{src.ID, CopyInbound{NodeID: 9}, ErrUnknownNode},
		{src.ID, CopyInbound{Port: "0"}, ErrBadPort},
		{src.ID, CopyInbound{Port: "70000"}, ErrBadPort},
		{src.ID, CopyInbound{Port: "2543-2000"}, ErrBadPort},
	} {
		if _, err := s.Copy(ctx, c.src, c.in); !errors.Is(err, c.want) {
			t.Errorf("%+v: got %v, want %v", c.in, err, c.want)
		}
	}
}
