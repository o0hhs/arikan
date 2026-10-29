package api

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/monitor"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/storetest"
)

const (
	testAdminPath   = "adminpath1234567"
	testSubPath     = "subpath12345"
	testAgentPath   = "agentpath12345"
	testMonitorPath = "monitorpath123"
)

// pathsStore is a settings table with every secret path set, the monitoring page's included.
func pathsStore(t *testing.T) (context.Context, *store.Store, *settings.Settings) {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st.Q)
	for k, v := range map[string]string{
		settings.KeyAdminPath: testAdminPath,
		settings.KeySubPath:   testSubPath,
		settings.KeyAgentPath: testAgentPath,
	} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := monitor.Save(ctx, set, monitor.Config{Enabled: true, Path: testMonitorPath, Hash: "a"}); err != nil {
		t.Fatal(err)
	}
	return ctx, st, set
}

// fieldCode is the code of a refused field, as the web reads it.
func fieldCode(t *testing.T, err error) string {
	t.Helper()
	var model *huma.ErrorModel
	if !errors.As(err, &model) || len(model.Errors) == 0 {
		t.Fatalf("not a field error: %v", err)
	}
	return model.Errors[0].Message
}

// The logs shown to the admin and to support are cut of every secret path, not only the
// admin's and the subscription's: the agent panel and the monitoring page are links too.
func TestSecretWordsCutEverySecretPath(t *testing.T) {
	ctx, _, set := pathsStore(t)
	words := (&handlers{d: Deps{Settings: set}}).secretWords(ctx)
	for _, want := range []string{testAdminPath, testSubPath, testAgentPath, testMonitorPath} {
		if !slices.Contains(words, want) {
			t.Errorf("%q is not cut out of the logs: %v", want, words)
		}
	}
}

// The router checks the monitoring page before the agent panel: a shared path would leave
// the agent panel's own URL answering the monitoring page.
func TestTheAgentPanelCannotTakeTheMonitorPath(t *testing.T) {
	ctx, _, set := pathsStore(t)
	err := (&handlers{d: Deps{Settings: set}}).checkAgentPath(ctx, testMonitorPath)
	if code := fieldCode(t, err); code != "agent_path_taken" {
		t.Fatalf("the monitoring page's path: %s", code)
	}
}

// And the monitoring page may not take a path the admin, the subscriptions or the agent
// panel answer on: the same clash from the other side.
func TestTheMonitorPageCannotTakeASecretPath(t *testing.T) {
	ctx, st, set := pathsStore(t)
	h := &handlers{d: Deps{Store: st, Settings: set}}
	for _, p := range []string{testAdminPath, testSubPath, testAgentPath} {
		in := &rotateMonitorInput{}
		in.Body.Path = p
		if _, err := h.rotateMonitorPath(ctx, in); fieldCode(t, err) != "monitor_path_taken" {
			t.Errorf("%q was taken by the monitoring page", p)
		}
	}
}
