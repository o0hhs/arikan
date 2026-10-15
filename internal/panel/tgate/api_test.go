package tgate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A message Telegram cannot parse goes again as plain text instead of failing; any other
// refusal stays an error.
func TestSendFallsBackToPlainText(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		switch {
		case body["parse_mode"] == "HTML":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: can't parse entities: Unsupported start tag \"x\" at byte offset 0"})
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5, "chat": map[string]any{"id": 1, "type": "private"}}})
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "1:x", nil)
	m, err := c.Send(context.Background(), 1, "<x>a &lt; b</x>", nil, false)
	if err != nil || m.MessageID != 5 || len(calls) != 2 || calls[1]["text"] != "a < b" || calls[1]["parse_mode"] != nil {
		t.Fatalf("send: %+v %v %v", m, err, calls)
	}
	calls = nil
	if err := c.Edit(context.Background(), 1, 5, "<x>c</x>", nil); err != nil || len(calls) != 2 || calls[1]["text"] != "c" {
		t.Fatalf("edit: %v %v", err, calls)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 403, "description": "Forbidden: bot was blocked by the user"})
	}))
	defer other.Close()
	if _, err := NewClient(other.URL, "1:x", nil).Send(context.Background(), 1, "a", nil, false); err == nil {
		t.Fatal("a blocked bot is not a parse error")
	}
}

// PlainText is what is left of the HTML when Telegram refuses it: the text, unescaped.
func TestPlainText(t *testing.T) {
	if got := PlainText(`<b>a &lt; b</b> <a href="https://e.com">c &amp; d</a> &#34;e&#34;`); got != `a < b c & d "e"` {
		t.Fatalf("plain %q", got)
	}
}

// A button's premium emoji Telegram will not take — a bot whose owner has no Premium — is
// what comes off, not the message: the menu arrives without it, the colors stay, and the
// refusal is told once.
func TestSendFallsBackWithoutEmoji(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		if hasEmoji(body) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400,
				"description": "Bad Request: can't use custom emoji in a button: the bot's owner needs Telegram Premium"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5, "chat": map[string]any{"id": 1, "type": "private"}}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "1:x", nil)
	var told [][]string
	var toldErr error
	c.OnEmojiRefused = func(ids []string, err error) { told = append(told, ids); toldErr = err }

	kb := &Keyboard{InlineKeyboard: [][]Button{
		{{Text: "a", CallbackData: "s", IconCustomEmojiID: "5368", Style: StylePrimary}, {Text: "b", CallbackData: "d"}},
		{{Text: "c", CallbackData: "r", IconCustomEmojiID: "9999"}},
	}}
	m, err := c.Send(context.Background(), 1, "меню", kb, false)
	if err != nil || m.MessageID != 5 || len(calls) != 2 {
		t.Fatalf("send: %+v %v %v", m, err, calls)
	}
	second := keyboard(t, calls[1])
	if hasEmoji(calls[1]) {
		t.Fatalf("the second call still carries the emoji: %v", second)
	}
	first := keyboard(t, calls[0])
	styled, _ := first[0].([]any)[0].(map[string]any)
	if styled["style"] != StylePrimary {
		t.Fatalf("the style must stay: %v", styled)
	}
	if len(told) != 1 || len(told[0]) != 2 || told[0][0] != "5368" || told[0][1] != "9999" || toldErr == nil {
		t.Fatalf("told: %v %v", told, toldErr)
	}
	// The keyboard the caller holds is untouched: the next send builds on the admin's menu.
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.Text == "a" && b.IconCustomEmojiID != "5368" || b.Text == "c" && b.IconCustomEmojiID != "9999" {
				t.Fatalf("the caller's keyboard was changed: %+v", kb)
			}
		}
	}
	calls = nil
	if err := c.Edit(context.Background(), 1, 5, "меню", kb); err != nil || len(calls) != 2 || hasEmoji(calls[1]) {
		t.Fatalf("edit: %v %v", err, calls)
	}

	// A refusal that names no emoji is no reason to send the menu crippled.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400,
			"description": "Bad Request: BUTTON_DATA_INVALID"})
	}))
	defer other.Close()
	oc := NewClient(other.URL, "1:x", nil)
	if _, err := oc.Send(context.Background(), 1, "меню", kb, false); err == nil {
		t.Fatal("a refusal of something else stays an error")
	}
}

// hasEmoji: the request's keyboard carries a button's premium emoji.
func hasEmoji(body map[string]any) bool {
	kb, _ := body["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			if m, ok := b.(map[string]any); ok && m["icon_custom_emoji_id"] != nil {
				return true
			}
		}
	}
	return false
}

func keyboard(t *testing.T, body map[string]any) []any {
	t.Helper()
	kb, _ := body["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	if len(rows) == 0 {
		t.Fatalf("no keyboard in %v", body)
	}
	return rows
}

// ValidEmojiID: an id is digits, twenty at most — what Telegram's are.
func TestValidEmojiID(t *testing.T) {
	for s, ok := range map[string]bool{"": true, "5368324170671202286": true, "0": true, "53683241706712022860": true,
		"536832417067120228601": false, "12a": false, "-1": false, " 1": false, "1 ": false} {
		if ValidEmojiID(s) != ok {
			t.Errorf("%q: %v", s, !ok)
		}
	}
}
