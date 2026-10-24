package botpanel

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"database/sql"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

func setInt(e *env, key string, v int64) error {
	return settings.Set(e.ctx, e.set, key, v)
}

// The bot's admin side: a stranger gets nothing, the owner setting makes a superadmin, the
// card carries the support's gifts, and the roles stay the superadmin's to give.
func TestAdminOfTheBot(t *testing.T) {
	e := setup(t)
	e.link(555)
	if err := setInt(e, KeyAdminOwner, 777); err != nil {
		t.Fatal(err)
	}

	// A stranger's /admin is answered by nothing.
	n := e.tg.count()
	e.say(555, "/admin")
	e.later()
	if e.tg.count() != n {
		t.Fatal("the bot answered an account that is not an admin")
	}

	// The owner's /admin is the menu.
	m := int64(1000)
	n = e.tg.count()
	e.press(777, m, "ad")
	menu, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(menu), "роль: superadmin") {
		t.Fatalf("admin menu: %q", text(menu))
	}
	if buttons(menu)["😴 Спящие триалы"] != "ad:t" || buttons(menu)["👥 Админы"] != "ad:a" {
		t.Fatalf("superadmin's buttons: %v", buttons(menu))
	}

	// The card by the id.
	id := strconv.FormatInt(e.user.ID, 10)
	n = e.tg.count()
	e.press(777, m, "ad:u:"+id)
	card, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(card), "Анна") || !strings.Contains(text(card), "#"+id) {
		t.Fatalf("card: %q", text(card))
	}
	if buttons(card)["➕ 3 дня"] != "ad:d:3:"+id || buttons(card)["🚫 Заблокировать"] != "ad:b:"+id {
		t.Fatalf("card buttons: %v", buttons(card))
	}

	// Three days land after the term in force.
	before, err := e.st.Q.GetUser(e.ctx, e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	n = e.tg.count()
	e.press(777, m, "ad:d:3:"+id)
	gifted, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	after, err := e.st.Q.GetUser(e.ctx, e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ExpiresAt.Int64-before.ExpiresAt.Int64 != 3*86400 || !strings.Contains(text(gifted), "3 дн.") {
		t.Fatalf("the gift: %d → %d, %q", before.ExpiresAt.Int64, after.ExpiresAt.Int64, text(gifted))
	}

	// The block and the way back.
	n = e.tg.count()
	e.press(777, m, "ad:b:"+id)
	e.tg.wait(t, n, "editMessageText")
	if u, _ := e.st.Q.GetUser(e.ctx, e.user.ID); u.Status != "disabled" {
		t.Fatalf("blocked status %q", u.Status)
	}
	n = e.tg.count()
	e.press(777, m, "ad:b:"+id)
	e.tg.wait(t, n, "editMessageText")
	if u, _ := e.st.Q.GetUser(e.ctx, e.user.ID); u.Status == "disabled" {
		t.Fatal("not unblocked")
	}

	// Support: the card, not the admins.
	if err := e.local.AdminSetRole(e.ctx, 888, "support", 777); err != nil {
		t.Fatal(err)
	}
	n = e.tg.count()
	e.press(888, m, "ad")
	sup, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(sup), "support") || buttons(sup)["👥 Админы"] != "" {
		t.Fatalf("support's menu: %q %v", text(sup), buttons(sup))
	}
}

// The sleeping trials: the accounts whose trial moved no traffic. The mailing extends each
// for three days and sends the connect button to each.
func TestSleepingTrialsMailing(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := setInt(e, KeyAdminOwner, 777); err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, tg := range []int64{555, 556} {
		u, err := e.users.Create(ctx, domain.CreateInput{Name: "trial" + strconv.FormatInt(tg, 10), TariffID: e.user.TariffID.Int64})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: tg, TariffID: sql.NullInt64{Int64: u.TariffID.Int64, Valid: true}, CreatedAt: e.now.Unix()}); err != nil {
			t.Fatal(err)
		}
		if err := e.st.Q.SetTrialUser(ctx, db.SetTrialUserParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}, TgID: tg}); err != nil {
			t.Fatal(err)
		}
		if err := e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: tg, CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	list, err := e.local.AdminSleepingTrials(ctx, 777)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("sleeping trials: %+v", list)
	}
	before := map[int64]int64{}
	for _, id := range ids {
		u, _ := e.st.Q.GetUser(ctx, id)
		before[id] = u.ExpiresAt.Int64
	}
	n := e.tg.count()
	e.press(777, 1000, "ad:run")
	e.tg.until(t, n, func(cs []call) bool {
		sent := 0
		for _, c := range cs {
			if c.method == "sendMessage" {
				sent++
			}
		}
		return sent >= 2
	})
	for _, id := range ids {
		u, _ := e.st.Q.GetUser(ctx, id)
		if u.ExpiresAt.Int64-before[id] != 3*86400 {
			t.Fatalf("trial %d: %d → %d", id, before[id], u.ExpiresAt.Int64)
		}
	}
}

// The panel itself refuses an admin call whose actor holds no role: the bot's screens are
// not the only gate, and an addon that holds the bot is not the actor.
func TestAdminCallsRefuseWithoutRole(t *testing.T) {
	e := setup(t)
	e.link(555)
	if err := setInt(e, KeyAdminOwner, 777); err != nil {
		t.Fatal(err)
	}
	ctx := e.ctx
	if _, err := e.local.AdminAddDays(ctx, 555, 1, 3); !errors.Is(err, errNoAdmin) {
		t.Fatalf("days for a stranger: %v", err)
	}
	if _, err := e.local.AdminSetDisabled(ctx, 555, 1, true); !errors.Is(err, errNoAdmin) {
		t.Fatalf("block by a stranger: %v", err)
	}
	if _, ok, err := e.local.AdminFindUser(ctx, 555, "555"); err == nil || ok {
		t.Fatalf("a stranger's search: %v %v", ok, err)
	}
	if _, err := e.local.AdminList(ctx, 555); !errors.Is(err, errNoAdmin) {
		t.Fatalf("the list for a stranger: %v", err)
	}
	if err := e.local.AdminDelRole(ctx, 777, 555); !errors.Is(err, errNoAdmin) {
		t.Fatalf("a stranger removing a role: %v", err)
	}
	// The support may act on a subscription, and only the superadmin names the roles.
	if _, err := e.local.AdminAddDays(ctx, 777, 1, 3); err != nil {
		t.Fatalf("the owner's days: %v", err)
	}
	if err := e.local.AdminSetRole(ctx, 888, "support", 777); err != nil {
		t.Fatalf("the owner naming support: %v", err)
	}
	if err := e.local.AdminDelRole(ctx, 888, 777); err != nil {
		t.Fatalf("the owner removing a role: %v", err)
	}
	if _, err := e.local.AdminList(ctx, 888); !errors.Is(err, errNoAdmin) {
		t.Fatalf("the list for support: %v", err)
	}
}
