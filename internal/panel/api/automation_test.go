package api

import (
	"encoding/json"
	"testing"

	"mikan/internal/panel/automation"
)

func TestAutomationRuleBodyDiscount(t *testing.T) {
	valid := json.RawMessage(`[{"type":"discount","percent":25,"discount_days":14,"text":"Ваш код: {code}"}]`)
	if _, _, err := automationRuleBody("Win-back", automation.TriggerUserInactive, nil, valid); err != nil {
		t.Fatalf("valid discount action: %v", err)
	}

	for name, raw := range map[string]json.RawMessage{
		"percent": json.RawMessage(`[{"type":"discount","percent":0,"discount_days":14}]`),
		"days":    json.RawMessage(`[{"type":"discount","percent":25,"discount_days":0}]`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := automationRuleBody("Win-back", automation.TriggerUserInactive, nil, raw); err == nil {
				t.Fatal("invalid discount action was accepted")
			}
		})
	}
}
