package proto_test

import (
	"strings"
	"testing"

	"mikan/internal/panel/presets"
	"mikan/internal/proto"
)

func TestRedactPresetConfigs(t *testing.T) {
	for _, p := range presets.All {
		if !p.Default {
			continue
		}
		cfg, err := presets.NewConfig(p.ID, presets.DefaultDest)
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		if _, err := proto.Parse(cfg); err != nil {
			t.Errorf("%s: generated config does not parse: %v\n%s", p.ID, err, cfg)
			continue
		}
		r := proto.Redact(cfg)
		if r == "" {
			t.Errorf("%s: redacted to nothing", p.ID)
		}
		if strings.Contains(r, "private-key\":\"") && !strings.Contains(r, proto.Hidden) {
			t.Errorf("%s: no hidden mark: %s", p.ID, r)
		}
	}
}
