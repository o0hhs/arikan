package api

import (
	"reflect"
	"strings"
	"testing"

	"mikan/internal/panel/presets"
)

// The create form's preset list is a struct tag huma reads, so it cannot be built from
// presets.All: a preset added to the catalog alone reaches the UI (the list it draws comes
// from the catalog) and meets a 422 on submit, as the CDN preset did for a release.
func TestCreateInboundPresetEnumMatchesTheCatalog(t *testing.T) {
	field, ok := reflect.TypeOf(createInboundInput{}.Body).FieldByName("Preset")
	if !ok {
		t.Fatal("createInboundInput.Body has no Preset field")
	}
	enum := map[string]bool{}
	for _, id := range strings.Split(field.Tag.Get("enum"), ",") {
		enum[id] = true
	}
	for _, p := range presets.All {
		if !enum[p.ID] {
			t.Errorf("preset %q is in the catalog but not in the enum of the create form: the API answers 422", p.ID)
		}
		delete(enum, p.ID)
	}
	for id := range enum {
		t.Errorf("the enum of the create form names %q, which the catalog does not", id)
	}
}
