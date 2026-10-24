package tgbot

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/model"
	"mikan/internal/tgapi"
)

// The bot's admin side: /admin for the accounts of admin_users (and the owner setting).
// Support keeps one subscriber's card: the days it gifts, the sessions it resets, the
// block. Sleeping trials go out as one mailing that extends them and says how to connect.
// The superadmin names the others. The screens are messages of their own, apart from the
// subscriber's menu.

// adm is ru or en by the bot's language.
func (w *words) adm(ru, enText string) string {
	if w == &en {
		return enText
	}
	return ru
}

// adminMessage handles the admin's plain messages: the commands and the search's input.
// handled false: not the admin's, on to the bot's own handling.
func (b *Bot) adminMessage(ctx context.Context, m *tgapi.Message, w *words) bool {
	chat := m.Chat.ID
	text := strings.TrimSpace(m.Text)
	switch {
	case text == "/admin":
		b.sendAdminMenu(ctx, w, chat)
		return true
	case strings.HasPrefix(text, "/admin_add "):
		b.adminAdd(ctx, w, chat, strings.TrimSpace(strings.TrimPrefix(text, "/admin_add ")))
		return true
	case strings.HasPrefix(text, "/admin_del "):
		b.adminDel(ctx, w, chat, strings.TrimSpace(strings.TrimPrefix(text, "/admin_del ")))
		return true
	}
	if v, ok := b.adminAwait.LoadAndDelete(chat); ok && v == "search" {
		b.adminCard(ctx, w, chat, text)
		return true
	}
	return false
}

// adminRole is the account's role; "" means it has none and the panel says nothing.
func (b *Bot) adminRole(ctx context.Context, chat int64) string {
	role, err := b.d.Backend.AdminRole(ctx, chat)
	if err != nil {
		return ""
	}
	return role
}

// sendNotice sends one admin message with its keyboard, off the update loop.
func (b *Bot) sendNotice(chat int64, text string, kb *tgapi.Keyboard) {
	b.d.Outbox.Notice(chat, func(ctx context.Context, c *tgapi.Client) error {
		_, err := c.Send(ctx, chat, text, kb, false)
		return err
	}, nil)
}

// adminMenu is what /admin answers: what the role may do.
func (b *Bot) adminMenu(ctx context.Context, w *words, chat int64) (string, *tgapi.Keyboard) {
	role := b.adminRole(ctx, chat)
	if role != "superadmin" && role != "support" {
		return "", nil // not the admin side's chat (tech holds no admin data): no answer
	}
	rows := [][]tgapi.Button{
		{{Text: w.adm("🔍 Найти пользователя", "🔍 Find a user"), CallbackData: "ad:s"}},
		{{Text: w.adm("😴 Спящие триалы", "😴 Sleeping trials"), CallbackData: "ad:t"}},
	}
	if role == "superadmin" {
		rows = append(rows, []tgapi.Button{{Text: w.adm("👥 Админы", "👥 Admins"), CallbackData: "ad:a"}})
	}
	return fmt.Sprintf(w.adm("🛠 Админка · роль: %s", "🛠 Admin · role: %s"), role), &tgapi.Keyboard{InlineKeyboard: rows}
}

// sendAdminMenu sends the menu as a message of its own.
func (b *Bot) sendAdminMenu(ctx context.Context, w *words, chat int64) {
	if text, kb := b.adminMenu(ctx, w, chat); text != "" {
		b.sendNotice(chat, text, kb)
	}
}

// adminAdd names an account: /admin_add <tg id> <superadmin|support|tech>.
func (b *Bot) adminAdd(ctx context.Context, w *words, chat int64, arg string) {
	if b.adminRole(ctx, chat) != "superadmin" {
		return
	}
	fields := strings.Fields(arg)
	if len(fields) != 2 {
		b.sendNotice(chat, w.adm("Пришлите: /admin_add <id> <superadmin|support|tech>", "Send: /admin_add <id> <superadmin|support|tech>"), nil)
		return
	}
	id, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || b.d.Backend.AdminSetRole(ctx, id, fields[1], chat) != nil {
		b.sendNotice(chat, w.adm("Не вышло: id числом и роль superadmin, support или tech.", "That did not work: an id and superadmin, support or tech."), nil)
		return
	}
	b.sendNotice(chat, fmt.Sprintf(w.adm("✅ %s теперь %s", "✅ %s is %s now"), fields[0], fields[1]), nil)
}

// adminDel takes an account off the list.
func (b *Bot) adminDel(ctx context.Context, w *words, chat int64, arg string) {
	if b.adminRole(ctx, chat) != "superadmin" {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil || b.d.Backend.AdminDelRole(ctx, id, chat) != nil {
		b.sendNotice(chat, w.adm("Не вышло: пришлите id.", "That did not work: send the id."), nil)
		return
	}
	b.sendNotice(chat, fmt.Sprintf(w.adm("✅ %s больше не админ", "✅ %s is off the list"), arg), nil)
}

// adminPress answers the admin menu's buttons.
func (b *Bot) adminPress(ctx context.Context, w *words, chat int64, arg string) (string, *tgapi.Keyboard) {
	if role := b.adminRole(ctx, chat); role != "superadmin" && role != "support" {
		return "", nil
	}
	switch arg {
	case "s":
		b.adminAwait.Store(chat, "search")
		return w.adm("🔍 Пришлите ID подписки, @username или номер платежа.", "🔍 Send the subscription id, an @username or a payment number."), &tgapi.Keyboard{InlineKeyboard: [][]tgapi.Button{{{Text: w.back, CallbackData: "ad"}}}}
	case "t":
		return b.sleepingTrials(ctx, w, chat)
	case "a":
		if b.adminRole(ctx, chat) != "superadmin" {
			return "", nil
		}
		list, err := b.d.Backend.AdminList(ctx, chat)
		if err != nil {
			return "", nil
		}
		lines := []string{w.adm("<b>👥 Админы</b>", "<b>👥 Admins</b>")}
		for _, a := range list {
			name := a.Username
			if name == "" {
				name = strconv.FormatInt(a.TgID, 10)
			}
			lines = append(lines, fmt.Sprintf("• <b>%s</b> · %s", html.EscapeString(name), a.Role))
		}
		lines = append(lines, "", w.adm("Добавить: /admin_add &lt;id&gt; &lt;superadmin|support|tech&gt;\nУбрать: /admin_del &lt;id&gt;", "Add: /admin_add &lt;id&gt; &lt;superadmin|support|tech&gt;\nRemove: /admin_del &lt;id&gt;"))
		return strings.Join(lines, "\n"), &tgapi.Keyboard{InlineKeyboard: [][]tgapi.Button{{{Text: w.back, CallbackData: "ad"}}}}
	}
	parts := strings.Split(arg, ":")
	switch parts[0] {
	case "u":
		if len(parts) < 2 {
			return "", nil
		}
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		return b.adminCardByID(ctx, w, chat, id)
	case "d":
		if len(parts) != 3 {
			return "", nil
		}
		days, _ := strconv.Atoi(parts[1])
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		if _, err := b.d.Backend.AdminAddDays(ctx, chat, id, days); err != nil {
			return w.adm("Не вышло: подписки нет.", "That did not work: no subscription."), nil
		}
		text, kb := b.adminCardByID(ctx, w, chat, id)
		return text + "\n\n" + fmt.Sprintf(w.adm("➕ %d дн. добавлено", "➕ %d days added"), days), kb
	case "r":
		if len(parts) < 2 {
			return "", nil
		}
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		if err := b.d.Backend.AdminResetSessions(ctx, chat, id); err != nil {
			return w.adm("Не вышло: подписки нет.", "That did not work: no subscription."), nil
		}
		text, kb := b.adminCardByID(ctx, w, chat, id)
		return text + "\n\n" + w.adm("🔄 Сессии сброшены, ссылка перевыпущена", "🔄 Sessions reset, the link reissued"), kb
	case "b":
		if len(parts) < 2 {
			return "", nil
		}
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		u, ok, err := b.d.Backend.AdminFindUser(ctx, chat, parts[1])
		if err != nil || !ok {
			return "", nil
		}
		disabled := u.Status != "disabled"
		if _, err := b.d.Backend.AdminSetDisabled(ctx, chat, id, disabled); err != nil {
			return "", nil
		}
		text, kb := b.adminCardByID(ctx, w, chat, id)
		what := w.adm("🚫 заблокирована", "🚫 blocked")
		if !disabled {
			what = w.adm("✅ разблокирована", "✅ unblocked")
		}
		return text + "\n\n" + what, kb
	case "run":
		if b.adminRole(ctx, chat) != "superadmin" && b.adminRole(ctx, chat) != "support" {
			return "", nil
		}
		go b.reactivateTrials(ctx, w, chat)
		return w.adm("📨 Рассылка пошла: продлеваю на 3 дня и пишу каждому.", "📨 The mailing is on: three days and a word to each."), nil
	}
	return "", nil
}

// adminCard is the subscriber's card by the search input.
func (b *Bot) adminCard(ctx context.Context, w *words, chat int64, query string) {
	u, ok, err := b.d.Backend.AdminFindUser(ctx, chat, query)
	if err != nil || !ok {
		b.sendNotice(chat, w.adm("Не нашёл. ID подписки, @username или номер платежа.", "Nothing found. A subscription id, an @username or a payment number."), nil)
		return
	}
	text, kb := b.adminCardOf(ctx, w, u)
	b.sendNotice(chat, text, kb)
}

// adminCardByID is the card of a known subscription.
func (b *Bot) adminCardByID(ctx context.Context, w *words, chat, id int64) (string, *tgapi.Keyboard) {
	u, ok, err := b.d.Backend.AdminFindUser(ctx, chat, strconv.FormatInt(id, 10))
	if err != nil || !ok {
		return w.adm("Не нашёл подписку.", "No such subscription."), nil
	}
	return b.adminCardOf(ctx, w, u)
}

// adminCardOf is one subscription as support sees it, with the quick actions.
func (b *Bot) adminCardOf(ctx context.Context, w *words, u model.User) (string, *tgapi.Keyboard) {
	tariff := "—"
	if u.TariffID.Valid {
		if t, err := b.d.Backend.Tariff(ctx, u.TariffID.Int64); err == nil {
			tariff = t.Name
		}
	}
	until := w.forever
	if u.ExpiresAt.Valid {
		until = w.date(model.Time(u.ExpiresAt.Int64))
	}
	traffic := w.noLimit
	if u.TrafficLimit.Valid {
		traffic = fmt.Sprintf("%s / %s", humanBytes(u.UsedUp+u.UsedDown), humanBytes(u.TrafficLimit.Int64))
	}
	devices, _ := b.d.Backend.DeviceCount(ctx, u.ID)
	limit := w.noLimit
	if u.DeviceLimit.Valid {
		limit = strconv.FormatInt(u.DeviceLimit.Int64, 10)
	}
	lines := []string{
		fmt.Sprintf("<b>%s</b> · #%d", html.EscapeString(u.Name), u.ID),
		"",
		fmt.Sprintf("%s: %s", w.adm("Статус", "Status"), u.Status),
		fmt.Sprintf("%s: %s", w.adm("Тариф", "Plan"), html.EscapeString(tariff)),
		fmt.Sprintf("%s: %s", w.adm("До", "Until"), until),
		fmt.Sprintf("%s: %s", w.adm("Трафик", "Traffic"), traffic),
		fmt.Sprintf("%s: %d/%s", w.adm("Устройства", "Devices"), devices, limit),
	}
	blocked := u.Status == "disabled"
	blockLabel := w.adm("🚫 Заблокировать", "🚫 Block")
	if blocked {
		blockLabel = w.adm("✅ Разблокировать", "✅ Unblock")
	}
	id := strconv.FormatInt(u.ID, 10)
	kb := &tgapi.Keyboard{InlineKeyboard: [][]tgapi.Button{
		{{Text: w.adm("➕ 3 дня", "➕ 3 days"), CallbackData: "ad:d:3:" + id}, {Text: w.adm("➕ 7 дней", "➕ 7 days"), CallbackData: "ad:d:7:" + id}},
		{{Text: w.adm("🔄 Сбросить сессии", "🔄 Reset sessions"), CallbackData: "ad:r:" + id}},
		{{Text: blockLabel, CallbackData: "ad:b:" + id}},
		{{Text: w.back, CallbackData: "ad"}},
	}}
	return strings.Join(lines, "\n"), kb
}

// sleepingTrials lists the accounts that took a trial and could not connect.
func (b *Bot) sleepingTrials(ctx context.Context, w *words, chat int64) (string, *tgapi.Keyboard) {
	list, err := b.d.Backend.AdminSleepingTrials(ctx, chat)
	if err != nil {
		return "", nil
	}
	if len(list) == 0 {
		return w.adm("Спящих триалов нет: все, кто взял, подключились.", "No sleeping trials: everyone who took one connected."), &tgapi.Keyboard{InlineKeyboard: [][]tgapi.Button{{{Text: w.back, CallbackData: "ad"}}}}
	}
	lines := []string{fmt.Sprintf(w.adm("<b>😴 Спящие триалы: %d</b>", "<b>😴 Sleeping trials: %d</b>"), len(list))}
	for i, t := range list {
		if i == 10 {
			lines = append(lines, "…")
			break
		}
		lines = append(lines, "• "+html.EscapeString(t.Name))
	}
	lines = append(lines, "", w.adm("Продлю на 3 дня и напишу каждому с кнопкой подключения.", "Each gets three days and a word with the connect button."))
	kb := &tgapi.Keyboard{InlineKeyboard: [][]tgapi.Button{
		{{Text: w.adm("✅ Продлить всем и разослать", "✅ Extend all and send"), CallbackData: "ad:run"}},
		{{Text: w.back, CallbackData: "ad"}},
	}}
	return strings.Join(lines, "\n"), kb
}

// reactivateTrials is the campaign: three days for each sleeping trial and a message with
// the connect button, a step of 50 ms apart — Telegram's flood limits are per second, and
// the outbox queues what it cannot send at once anyway.
func (b *Bot) reactivateTrials(ctx context.Context, w *words, chat int64) {
	if !b.trials.TryLock() {
		return // a campaign is already running: one button, one mailing
	}
	defer b.trials.Unlock()
	list, err := b.d.Backend.AdminSleepingTrials(ctx, chat)
	if err != nil {
		return
	}
	sent := 0
	kb := &tgapi.Keyboard{InlineKeyboard: [][]tgapi.Button{{{Text: w.connectButton, CallbackData: "c", Style: tgapi.StylePrimary}}}}
	for _, t := range list {
		if _, err := b.d.Backend.AdminAddDays(ctx, chat, t.UserID, 3); err != nil {
			continue
		}
		text := w.adm("🎁 Мы продлили ваш пробный период на 3 дня.\n\nПодключиться — одна кнопка ниже; если не вышло, напишите в поддержку.", "🎁 Your trial is three days longer.\n\nOne tap below to connect; if it does not work, write to support.")
		b.d.Outbox.Notice(t.TgID, func(ctx context.Context, c *tgapi.Client) error {
			_, err := c.Send(ctx, t.TgID, text, kb, false)
			return err
		}, nil)
		sent++
		time.Sleep(50 * time.Millisecond)
	}
	b.sendNotice(chat, fmt.Sprintf(w.adm("✅ Продлено и отправлено: %d", "✅ Extended and sent: %d"), sent), nil)
}

// humanBytes is a byte count the admin reads: 12.3 GB.
func humanBytes(n int64) string {
	const gb = int64(1) << 30
	const mb = int64(1) << 20
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(mb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
