package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// The support's card: one subscriber by a search input — an id, an @username or a payment's
// number — with the status, and three things to do about it: the bonus days, the sessions
// and the block. Nothing else of the panel is open to the role.

// SupportCard is one subscription as the support sees it.
type SupportCard struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Contact      string     `json:"contact,omitempty"`
	Status       string     `json:"status"`
	State        string     `json:"state" doc:"active, expiring, limited, expired, disabled"`
	Tariff       string     `json:"tariff,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty" doc:"null — бессрочно"`
	TrafficUsed  int64      `json:"traffic_used" doc:"Байты за период"`
	TrafficLimit *int64     `json:"traffic_limit,omitempty" doc:"null — без лимита"`
	Devices      int64      `json:"devices"`
	DeviceLimit  *int64     `json:"device_limit,omitempty"`
	TgUsername   string     `json:"tg_username,omitempty"`
	Blocked      bool       `json:"blocked"`
}

type supportCardOutput struct{ Body SupportCard }

type supportFindInput struct {
	Query string `query:"q" minLength:"1" maxLength:"128" doc:"id подписки, @username или номер платежа"`
}

type supportDaysInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Days int `json:"days" minimum:"1" maximum:"365"`
	}
}

type supportBlockedInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Blocked bool `json:"blocked"`
	}
}

func (h *handlers) registerSupportCard() {
	tags := []string{"support"}
	huma.Register(h.api, huma.Operation{OperationID: "support-find", Method: http.MethodGet, Path: "/api/v1/support/card", Summary: "Карточка клиента для поддержки", Tags: tags}, h.supportFind)
	huma.Register(h.api, huma.Operation{OperationID: "support-card-get", Method: http.MethodGet, Path: "/api/v1/support/card/{id}", Summary: "Карточка клиента по id", Tags: tags}, h.supportCardByID)
	huma.Register(h.api, huma.Operation{OperationID: "support-add-days", Method: http.MethodPost, Path: "/api/v1/support/card/{id}/days", Summary: "Начислить дни поддержкой", Tags: tags}, h.supportAddDays)
	huma.Register(h.api, huma.Operation{OperationID: "support-reset-sessions", Method: http.MethodPost, Path: "/api/v1/support/card/{id}/sessions", Summary: "Сбросить сессии: новая ссылка, устройства отвязаны", Tags: tags}, h.supportResetSessions)
	huma.Register(h.api, huma.Operation{OperationID: "support-block", Method: http.MethodPost, Path: "/api/v1/support/card/{id}/blocked", Summary: "Заблокировать или разблокировать", Tags: tags}, h.supportBlock)
}

// supportCardOf builds the card.
func (h *handlers) supportCardOf(ctx context.Context, u db.User) (SupportCard, error) {
	c := SupportCard{ID: u.ID, Name: u.Name, Contact: u.Contact, Status: u.Status, TrafficUsed: u.UsedUp + u.UsedDown,
		Devices: 0, Blocked: u.Status == "disabled"}
	grants, err := domain.UserGrantsLeft(ctx, h.d.Store.Q, u.ID, h.d.Now())
	if err != nil {
		return c, err
	}
	c.State = domain.State(u, grants.Main(u.ID), h.d.Now())
	if u.TariffID.Valid {
		if t, err := h.d.Store.Q.GetTariff(ctx, u.TariffID.Int64); err == nil {
			c.Tariff = t.Name
		}
	}
	if u.ExpiresAt.Valid {
		at := time.Unix(u.ExpiresAt.Int64, 0)
		c.ExpiresAt = &at
	}
	if u.TrafficLimit.Valid {
		v := u.TrafficLimit.Int64
		c.TrafficLimit = &v
	}
	if u.DeviceLimit.Valid {
		v := u.DeviceLimit.Int64
		c.DeviceLimit = &v
	}
	if n, err := h.d.Store.Q.CountBoundDevices(ctx, u.ID); err == nil {
		c.Devices = n
	}
	if link, err := h.d.Store.Q.TgLinkOfUser(ctx, u.ID); err == nil {
		c.TgUsername = link.Username
	}
	return c, nil
}

// supportFind is the search: an id, a payment's number or an @username.
func (h *handlers) supportFind(ctx context.Context, in *supportFindInput) (*supportCardOutput, error) {
	u, ok, err := h.supportUser(ctx, in.Query)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, huma.Error404NotFound("not_found")
	}
	c, err := h.supportCardOf(ctx, u)
	if err != nil {
		return nil, err
	}
	return &supportCardOutput{Body: c}, nil
}

// supportUser resolves the search input to a subscription, the way the bot's admin finds
// one: an id, a payment's number, an @username (a first name too).
func (h *handlers) supportUser(ctx context.Context, query string) (db.User, bool, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return db.User{}, false, nil
	}
	if id, err := strconv.ParseInt(strings.TrimPrefix(q, "@"), 10, 64); err == nil {
		if u, err := h.d.Store.Q.GetUser(ctx, id); err == nil {
			return u, true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return db.User{}, false, err
		}
		if u, err := h.d.Store.Q.FindUserByPayment(ctx, id); err == nil {
			return u, true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return db.User{}, false, err
		}
	}
	name := strings.TrimPrefix(q, "@")
	if name == "" {
		return db.User{}, false, nil
	}
	u, err := h.d.Store.Q.FindUserByTgUsername(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return db.User{}, false, nil
	}
	return u, err == nil, err
}

func (h *handlers) supportCardByID(ctx context.Context, in *userIDInput) (*supportCardOutput, error) {
	u, err := h.d.Store.Q.GetUser(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	c, err := h.supportCardOf(ctx, u)
	if err != nil {
		return nil, err
	}
	return &supportCardOutput{Body: c}, nil
}

// supportAddDays is the support's gift: days after the end in force, from now when it has
// passed; unlimited stays unlimited. The nodes hear of it at once.
func (h *handlers) supportAddDays(ctx context.Context, in *supportDaysInput) (*supportCardOutput, error) {
	u, err := h.d.Store.Q.ExtendUser(ctx, db.ExtendUserParams{ID: in.ID, Days: int64(in.Body.Days), Now: h.d.Now().Unix()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.d.Users.Changed()
	h.audit(ctx, sessionOf(ctx).AdminID, "support.days", "user", strconv.FormatInt(u.ID, 10), map[string]any{"days": in.Body.Days})
	c, err := h.supportCardOf(ctx, u)
	if err != nil {
		return nil, err
	}
	return &supportCardOutput{Body: c}, nil
}

// supportResetSessions takes every device off and issues a new link: the old one stops
// working, the apps must be set up again.
func (h *handlers) supportResetSessions(ctx context.Context, in *userIDInput) (*supportCardOutput, error) {
	u, err := h.d.Store.Q.GetUser(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	if _, err := h.d.Store.Q.DeleteUserBoundDevices(ctx, in.ID); err != nil {
		return nil, err
	}
	if u, err = h.d.Users.ReissueByOwner(ctx, in.ID, "admin"); err != nil {
		return nil, err
	}
	h.d.Users.Changed()
	h.audit(ctx, sessionOf(ctx).AdminID, "support.sessions", "user", strconv.FormatInt(u.ID, 10), nil)
	c, err := h.supportCardOf(ctx, u)
	if err != nil {
		return nil, err
	}
	return &supportCardOutput{Body: c}, nil
}

// supportBlock blocks the subscription (its apps get nothing) or lets it back.
func (h *handlers) supportBlock(ctx context.Context, in *supportBlockedInput) (*supportCardOutput, error) {
	status := "active"
	if in.Body.Blocked {
		status = "disabled"
	}
	u, err := h.d.Store.Q.SetUserDisabled(ctx, db.SetUserDisabledParams{ID: in.ID, Status: status, Now: h.d.Now().Unix()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.d.Users.Changed()
	h.audit(ctx, sessionOf(ctx).AdminID, "support.block", "user", strconv.FormatInt(u.ID, 10), map[string]any{"blocked": in.Body.Blocked})
	c, err := h.supportCardOf(ctx, u)
	if err != nil {
		return nil, err
	}
	return &supportCardOutput{Body: c}, nil
}
