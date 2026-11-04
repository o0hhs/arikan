package automation

import (
	"slices"
	"testing"

	"mikan/internal/panel/events"
)

// Every event the panel documents can be a rule's trigger. A new event that is not added
// here is invisible in the automation constructor.
func TestTriggersCoverEveryDocumentedEvent(t *testing.T) {
	for _, d := range events.Docs {
		if !slices.Contains(Triggers, d.Type) {
			t.Errorf("%s is documented but not a trigger", d.Type)
		}
	}
	if len(Triggers) != len(events.Docs) {
		t.Fatalf("%d triggers for %d documented events", len(Triggers), len(events.Docs))
	}
}
