package subs

import (
	"context"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
)

// noticeTG is the bot as the notice asks it: a link to the subscription's own chat.
type noticeTG struct{ link string }

func (n noticeTG) LinkURL(context.Context, int64) string { return n.link }
func (noticeTG) MiniAppUser(context.Context, string) (int64, []db.User, error) {
	return 0, nil, nil
}

// TestNoticeText: the entry in place of an ended subscription says why in the subscription's
// own language, and carries the bot's link when there is a bot (the start code cut off — a
// profile reads it as a plain address).
func TestNoticeText(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct {
		name  string
		lang  string
		state string
		tg    Telegram
		want  []string
		not   []string
	}{
		{name: "expired ru", lang: "", state: "expired", want: []string{"Подписка закончилась", "продлите"}},
		{name: "expired en", lang: "en", state: "expired", want: []string{"The subscription has ended", "renew"}},
		{name: "limited ru", lang: "", state: "limited", want: []string{"Трафик закончился"}},
		{name: "limited en", lang: "en", state: "limited", want: []string{"traffic is used up"}},
		{
			name: "with the bot", lang: "en", state: "expired", tg: noticeTG{link: "https://t.me/example_bot?start=abc.def"},
			want: []string{"https://t.me/example_bot"}, not: []string{"?start="},
		},
		{name: "bot without a link", lang: "en", state: "expired", tg: noticeTG{}, not: []string{"t.me"}},
		{name: "no bot", lang: "en", state: "expired", not: []string{"t.me"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.tg = tc.tg
			got := h.noticeText(context.Background(), db.User{ID: 7}, tc.state, Config{Lang: tc.lang})
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("notice %q does not hold %q", got, want)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(got, not) {
					t.Errorf("notice %q holds %q", got, not)
				}
			}
		})
	}
}
