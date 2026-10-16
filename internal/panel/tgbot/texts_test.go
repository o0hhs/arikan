package tgbot

import (
	"reflect"
	"testing"
)

// Every word the bot has is there in both languages: a field filled in one and left empty
// in the other is a screen that shows nothing there.
func TestWordsBothLanguages(t *testing.T) {
	vru, ven := reflect.ValueOf(ru), reflect.ValueOf(en)
	tp := reflect.TypeOf(ru)
	for i := range tp.NumField() {
		f := tp.Field(i)
		switch f.Type.Kind() {
		case reflect.String:
			r, e := vru.Field(i).String(), ven.Field(i).String()
			if r == "" || e == "" {
				t.Errorf("%s: ru %q, en %q", f.Name, r, e)
			}
		case reflect.Array: // the months
			for j := range f.Type.Len() {
				r, e := vru.Field(i).Index(j).String(), ven.Field(i).Index(j).String()
				if r == "" || e == "" {
					t.Errorf("%s[%d]: ru %q, en %q", f.Name, j, r, e)
				}
			}
		}
	}
}
