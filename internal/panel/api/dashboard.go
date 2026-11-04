package api

import (
	"context"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/dashboard"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The dashboard as the admin sets it up: which cards it shows and how it dates them. The
// clock is the visitor's; the format is this panel's.

type dashboardOutput struct{ Body dashboard.Layout }

type putDashboardInput struct {
	Body dashboard.Layout
}

func (h *handlers) registerDashboard() {
	tags := []string{"settings"}
	huma.Register(h.api, huma.Operation{OperationID: "get-dashboard", Method: http.MethodGet, Path: "/api/v1/dashboard", Summary: "Какие карточки показывает главная и как пишет дату", Tags: tags}, h.getDashboard)
	huma.Register(h.api, huma.Operation{OperationID: "put-dashboard", Method: http.MethodPut, Path: "/api/v1/dashboard", Summary: "Настроить главную: карточки и дату с временем", Tags: tags}, h.putDashboard)
}

func (h *handlers) getDashboard(ctx context.Context, _ *struct{}) (*dashboardOutput, error) {
	c, err := dashboard.Load(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	return &dashboardOutput{Body: c}, nil
}

// putDashboard takes the document whole: a card the admin switched off is off, and a card
// this panel does not know is refused rather than stored and forgotten.
func (h *handlers) putDashboard(ctx context.Context, in *putDashboardInput) (*dashboardOutput, error) {
	var details []error
	for card := range in.Body.Cards {
		if !slices.Contains(dashboard.Cards, card) {
			details = append(details, fieldDetail("body.cards."+card, "dashboard_card_unknown", card))
		}
	}
	if !slices.Contains(dashboard.Dates, in.Body.Date) {
		details = append(details, fieldDetail("body.date", "dashboard_date_unknown", in.Body.Date))
	}
	if len(details) > 0 {
		return nil, fieldErrs(details...)
	}
	// Only the cards this panel has: what the request said about the rest is not stored.
	c := dashboard.Default()
	for card := range c.Cards {
		if on, ok := in.Body.Cards[card]; ok {
			c.Cards[card] = on
		}
	}
	c.Date, c.Seconds = in.Body.Date, in.Body.Seconds
	if err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		return settings.Set(ctx, settings.New(q), settings.KeyDashboard, c)
	}); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "dashboard.update", "settings", settings.KeyDashboard, map[string]any{"cards": c.Cards, "date": c.Date, "seconds": c.Seconds})
	return &dashboardOutput{Body: c}, nil
}
