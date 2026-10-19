// Package dashboard is the panel's own front page as the admin sets it up: which cards it
// shows and how it writes the date and the time (one settings document,
// settings.KeyDashboard). Nothing configured is the dashboard as it always was: Default.
//
// The clock is the visitor's: the format is the admin's, the time zone and the language are
// the browser's, so the date the admin sees on their laptop is the one their server shows
// them in their own time.
package dashboard

import (
	"context"
	"slices"

	"mikan/internal/panel/settings"
)

// Cards are the dashboard's blocks, as the page lays them out. The names are the page's:
// kpis (online, active, traffic, expiring), traffic (the chart), attention (what needs the
// admin), top (the month's busiest), server (the node's own numbers), nodes (how the
// traffic is shared between them).
var Cards = []string{"kpis", "traffic", "attention", "top", "server", "nodes"}

// Dates are how the dashboard writes the day: compact — the weekday and the date, as it
// always was; full — the date and the time; large — the same, as the page's heading.
var Dates = []string{"compact", "full", "large"}

const (
	// DefaultDate is the date as it always was.
	DefaultDate = "compact"
	// DefaultSeconds: with a date that carries the time, it is written to the second.
	DefaultSeconds = true
	// Breaking is the width the Large date is written for: the panel's layout switches
	// there, and the heading holds its line.
	Breaking = 380
)

// Layout is the dashboard's settings document: which cards it shows and how it writes the
// date. The API takes and gives it whole.
type Layout struct {
	// Cards: what the page shows. A card nobody named is shown: a card added by a later
	// version appears as it should, without the admin setting it up again.
	Cards   map[string]bool `json:"cards" doc:"Какие карточки показывает главная"`
	Date    string          `json:"date" enum:"compact,full,large" doc:"compact — день недели и дата, full — дата и время, large — то же крупно"`
	Seconds bool            `json:"seconds" doc:"Писать время до секунды (для full и large)"`
}

// Default is what the dashboard shows with nothing set: every card, the date as always.
func Default() Layout {
	c := Layout{Cards: make(map[string]bool, len(Cards)), Date: DefaultDate, Seconds: DefaultSeconds}
	for _, k := range Cards {
		c.Cards[k] = true
	}
	return c
}

// Load reads the document, filling what it does not say: a card nobody named is shown, a
// date this panel does not know is the default one. Reading never fails on what is stored:
// a document from an older or a hand-edited panel gives the dashboard it describes, what it
// cannot mean left out.
func Load(ctx context.Context, set *settings.Settings) (Layout, error) {
	stored, ok, err := settings.Get[Layout](ctx, set, settings.KeyDashboard)
	if err != nil || !ok {
		return Default(), err
	}
	out := Default()
	for k, on := range stored.Cards {
		if slices.Contains(Cards, k) {
			out.Cards[k] = on
		}
	}
	if slices.Contains(Dates, stored.Date) {
		out.Date = stored.Date
	}
	out.Seconds = stored.Seconds
	return out, nil
}

// Show says whether the named card is shown.
func (c Layout) Show(card string) bool { return c.Cards[card] }
