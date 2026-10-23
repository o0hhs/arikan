package tgbot

import (
	"strings"
	"testing"
)

// The answer to the admin's emoji is in both languages, names the id and says what it is
// for; a Premium subscription is what makes it show.
func TestEmojiTexts(t *testing.T) {
	for name, w := range map[string]*words{"ru": &ru, "en": &en} {
		if !strings.Contains(w.emojiID, "%s") || !strings.Contains(w.emojiID, "Premium") || strings.ContainsAny(w.emojiID, "—–") {
			t.Errorf("%s: emojiID %q", name, w.emojiID)
		}
	}
}
