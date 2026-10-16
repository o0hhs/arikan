package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/store/db"
)

// The source the panel ships with shows in the admin's list as built-in, and is not theirs to
// remove: it would be readded at the next start, and this fork's bot comes from it. A source
// of the admin's own is still theirs to remove.
func TestBuiltinSourceCannotBeRemoved(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.DataDir = t.TempDir() })
	ctx := context.Background()
	if err := h.p.Addons.EnsureBuiltinSource(ctx, h.st.Q, h.p.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Q.AddAddonSource(ctx, db.AddAddonSourceParams{Url: "github.com/someone/else", Title: "else", AddedAt: h.p.now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	route := "/" + adminPath + "/api/v1/addon-sources"

	resp, body := h.do(http.MethodGet, route, nil, nil)
	var list struct {
		Sources []struct {
			ID      int64  `json:"id"`
			URL     string `json:"url"`
			Builtin bool   `json:"builtin"`
		} `json:"sources"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list.Sources) != 2 {
		t.Fatalf("sources: %d %s", resp.StatusCode, body)
	}
	for _, s := range list.Sources {
		builtin := addons.IsBuiltinSource(s.URL)
		if s.Builtin != builtin {
			t.Fatalf("%s: builtin %v", s.URL, s.Builtin)
		}
		resp, body := h.do(http.MethodDelete, route+"/"+strconv.FormatInt(s.ID, 10), nil, csrf)
		if builtin {
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("removing the panel's own source: %d %s", resp.StatusCode, body)
			}
			continue
		}
		if resp.StatusCode/100 != 2 {
			t.Fatalf("removing an admin's source: %d %s", resp.StatusCode, body)
		}
	}
	rows, err := h.st.Q.ListAddonSources(ctx)
	if err != nil || len(rows) != 1 || !addons.IsBuiltinSource(rows[0].Url) {
		t.Fatalf("what is left: %+v %v", rows, err)
	}
}
