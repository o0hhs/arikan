package tgbot

import (
	"context"
	"fmt"
	"mikan/internal/tgapi"
	"slices"
	"strings"
)

// The premium emoji the admin gives a menu button (MenuButton.EmojiID): a custom emoji's id,
// shown before the label in clients new enough for it. Telegram takes one only from a bot
// whose owner has Premium — or whose owner bought usernames on Fragment — and refuses it
// from any other. The send then goes out without the emoji (tgapi.Client's fallback), and
// the addon tells the panel here, so the admin's page can say why the menu has none.

// KeyEmojiRefused is where the panel keeps Telegram's last refusal (EmojiRefusal).
const KeyEmojiRefused = "tg_emoji_refused"

// EmojiRefusal: Telegram would not take these emoji ids, in its own words. The admin's page
// shows it beside the menu; saving the menu again clears it.
type EmojiRefusal struct {
	IDs []string `json:"ids"`
	// Error is Telegram's description of the refusal, "" when none was told.
	Error string `json:"error,omitempty"`
	At    int64  `json:"at"`
}

// unique keeps the first of each id, so a menu with one emoji on three buttons names it once.
func unique(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// emojiIDs are the premium emoji of a message sent to the bot: what its "custom_emoji"
// entities carry.
func emojiIDs(m *tgapi.Message) []string {
	var ids []string
	for _, e := range m.Entities {
		if e.Type == "custom_emoji" && tgapi.ValidEmojiID(e.CustomEmojiID) && !slices.Contains(ids, e.CustomEmojiID) {
			ids = append(ids, e.CustomEmojiID)
		}
	}
	return ids
}

// emojiHelp answers the panel's admin, who sent or forwarded the bot a premium emoji, with
// its id: the one thing needed to put that emoji on a menu button, and the one thing
// Telegram shows nowhere. Only that chat is answered; in the bot's other chats an emoji in a
// message is an emoji in a message.
func (b *Bot) emojiHelp(ctx context.Context, m *tgapi.Message, w *words) bool {
	if len(m.Entities) == 0 || m.Chat.ID == 0 || m.Chat.ID != b.d.Backend.AdminChat(ctx) {
		return false
	}
	ids := emojiIDs(m)
	if len(ids) == 0 {
		return false
	}
	list := make([]string, 0, len(ids))
	for _, id := range ids {
		list = append(list, "<code>"+id+"</code>")
	}
	text := fmt.Sprintf(w.emojiID, strings.Join(list, " "))
	b.d.Outbox.Notice(m.Chat.ID, func(ctx context.Context, c *tgapi.Client) error {
		_, err := c.Send(ctx, m.Chat.ID, text, nil, false)
		return err
	}, nil)
	return true
}
