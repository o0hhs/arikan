package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"mikan/internal/panel/store/db"
)

// The invites list names who brought the account in and what came of them — the link
// followed, the trial taken, a subscription of their own or a payment — and the totals say
// how many took nothing at all: an invitee who followed the link and left was only a code
// on the list before.
func TestReferralLinksNameTheReferrerAndTheStage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.now.Unix()
	const (
		inviter  = int64(101)
		trialist = int64(202)
		sleeper  = int64(303)
	)
	if err := h.st.Q.UpsertTgChat(ctx, db.UpsertTgChatParams{TgID: inviter, Username: "petya777", FirstName: "Пётр", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// An invitee without a username is called by the name the bot saw.
	if err := h.st.Q.UpsertTgChat(ctx, db.UpsertTgChatParams{TgID: trialist, FirstName: "Вася", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if n, err := h.st.Q.TakeReferralAccount(ctx, db.TakeReferralAccountParams{TgID: inviter, Code: "NHpa4rrR", CreatedAt: now}); err != nil || n != 1 {
		t.Fatalf("account: %d %v", n, err)
	}
	for _, tg := range []int64{trialist, sleeper} {
		if n, err := h.st.Q.LinkReferral(ctx, db.LinkReferralParams{ReferrerTgID: inviter, RefereeTgID: tg, Code: "NHpa4rrR", CreatedAt: now}); err != nil || n != 1 {
			t.Fatalf("bind %d: %d %v", tg, n, err)
		}
	}
	if n, err := h.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: trialist, CreatedAt: now}); err != nil || n != 1 {
		t.Fatalf("trial: %d %v", n, err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	api := "/" + adminPath + "/api/v1"

	resp, body := h.do(http.MethodGet, api+"/referrals/links", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("links: %d %s", resp.StatusCode, body)
	}
	var links struct {
		Items []struct {
			RefereeName  string `json:"referee_name"`
			ReferrerName string `json:"referrer_name"`
			ReferrerCode string `json:"referrer_code"`
			Stage        string `json:"stage"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(body, &links); err != nil {
		t.Fatalf("links json: %v %s", err, body)
	}
	if links.Total != 2 || len(links.Items) != 2 {
		t.Fatalf("links %+v", links)
	}
	// Newest first: the one who took nothing, then the one who took the trial.
	slept, took := links.Items[0], links.Items[1]
	if slept.RefereeName != "" || slept.Stage != "invited" || slept.ReferrerName != "@petya777" || slept.ReferrerCode != "NHpa4rrR" {
		t.Fatalf("the sleeper: %+v", slept)
	}
	if took.RefereeName != "Вася" || took.Stage != "trial" || took.ReferrerName != "@petya777" {
		t.Fatalf("the trialist: %+v", took)
	}

	resp, body = h.do(http.MethodGet, api+"/referrals", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("programme: %d %s", resp.StatusCode, body)
	}
	var view struct {
		Stats struct {
			Invited int64 `json:"invited"`
			Trial   int64 `json:"trial"`
			Paid    int64 `json:"paid"`
			Idle    int64 `json:"idle"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("programme json: %v %s", err, body)
	}
	if view.Stats.Invited != 2 || view.Stats.Trial != 1 || view.Stats.Paid != 0 || view.Stats.Idle != 1 {
		t.Fatalf("stats %+v", view.Stats)
	}
}
