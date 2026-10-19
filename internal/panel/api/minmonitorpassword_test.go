package api

import (
	"reflect"
	"strconv"
	"testing"

	"mikan/internal/panel/auth"
)

// The monitoring page's password form repeats auth.MinPassword in its struct tag, which
// huma validates with: a shorter password never reaches the handler, so the two must agree.
func TestMonitorPasswordTagIsMinPassword(t *testing.T) {
	field, ok := reflect.TypeOf(setMonitorPasswordInput{}).FieldByName("Body")
	if !ok {
		t.Fatal("no Body field in the password form")
	}
	f, ok := field.Type.FieldByName("Password")
	if !ok {
		t.Fatal("no Password field in the password form")
	}
	if got := f.Tag.Get("minLength"); got != strconv.Itoa(auth.MinPassword) {
		t.Fatalf("minLength:%q, auth.MinPassword is %d", got, auth.MinPassword)
	}
}
