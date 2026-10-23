package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/discounts"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// DiscountView is a discount as the admin's page shows it: the sale itself and its mailing.
type DiscountView struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Type        string    `json:"type" enum:"percent,fixed" doc:"percent — процент от цены, fixed — сумма скидки"`
	Value       int64     `json:"value"`
	Currency    string    `json:"currency" doc:"Для fixed: RUB (копейки) или XTR (Stars). Для percent: пусто — любая валюта"`
	TariffIDs   []int64   `json:"tariff_ids" doc:"Тарифы, к которым applies скидка; пусто — ко всем тарифам в продаже"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	MaxUses     *int64    `json:"max_uses,omitempty" doc:"Сколько раз скидку можно применить; без значения — без ограничения"`
	UsedCount   int64     `json:"used_count"`
	MinOrder    int64     `json:"min_order" doc:"Минимальная сумма заказа, в минимальных единицах оплаты"`
	MaxDiscount int64     `json:"max_discount" doc:"Максимальная скидка, в минимальных единицах оплаты"`
	// The mailing: whether the bot reminds about the end of the sale, how long before the
	// end it says so and to whom.
	NotifyEnabled  bool      `json:"notify_enabled"`
	NotifyDays     []int     `json:"notify_days" doc:"За сколько дней до конца скидки бот напоминает"`
	NotifyAudience string    `json:"notify_audience" enum:"buyers,subscribers" doc:"buyers — тем, кто купил по скидке; subscribers — всем подписчикам тарифов скидки"`
	NotifyText     string    `json:"notify_text" doc:"Свой текст рассылки; пусто — текст бота"`
	Enabled        bool      `json:"enabled"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	CreatedBy      *int64    `json:"created_by,omitempty"`
	// Recipients is how many accounts the mailing goes to now.
	Recipients int64 `json:"recipients"`
}

// DiscountBody is a discount as the admin saves it.
type DiscountBody struct {
	Name        string  `json:"name" minLength:"1" maxLength:"120"`
	Description string  `json:"description" maxLength:"500"`
	Type        string  `json:"type" enum:"percent,fixed"`
	Value       int64   `json:"value" minimum:"1" doc:"percent — проценты 1-100; fixed — сумма в минимальных единицах оплаты"`
	Currency    string  `json:"currency" doc:"Для fixed: RUB или XTR. Для percent: пусто — любая валюта"`
	TariffIDs   []int64 `json:"tariff_ids"`
	StartsAt    int64   `json:"starts_at" doc:"Unix time в секундах"`
	EndsAt      int64   `json:"ends_at" doc:"Unix time в секундах; значение не может быть в прошлом"`
	MaxUses     *int64  `json:"max_uses,omitempty" minimum:"1"`
	MinOrder    int64   `json:"min_order" minimum:"0"`
	MaxDiscount int64   `json:"max_discount" minimum:"0"`
	// The mailing.
	NotifyEnabled  bool   `json:"notify_enabled" doc:"Напоминать ли о конце скидки"`
	NotifyDays     []int  `json:"notify_days" doc:"За сколько дней до конца скидки напоминать; по умолчанию за 3 дня и за день"`
	NotifyAudience string `json:"notify_audience" enum:"buyers,subscribers"`
	NotifyText     string `json:"notify_text" maxLength:"1000"`
	Enabled        bool   `json:"enabled"`
}

type discountListInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

type discountListOutput struct {
	Body struct {
		Items []DiscountView `json:"items"`
		Total int64          `json:"total"`
	}
}

type discountOneOutput struct{ Body DiscountView }

// DiscountRedemptionView is one order at the lower price.
type DiscountRedemptionView struct {
	ID             int64     `json:"id"`
	DiscountID     int64     `json:"discount_id"`
	DiscountName   string    `json:"discount_name"`
	UserID         *int64    `json:"user_id,omitempty"`
	TgID           int64     `json:"tg_id"`
	PaymentID      *int64    `json:"payment_id,omitempty"`
	Status         string    `json:"status"`
	RedeemedAt     time.Time `json:"redeemed_at"`
	DiscountAmount int64     `json:"discount_amount"`
	OriginalAmount int64     `json:"original_amount"`
	FinalAmount    int64     `json:"final_amount"`
	Currency       string    `json:"currency"`
}

type discountRedemptionsInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

type discountRedemptionsOutput struct {
	Body struct {
		Items []DiscountRedemptionView `json:"items"`
		Total int64                    `json:"total"`
	}
}

func (h *handlers) registerDiscounts() {
	tags := []string{"discounts"}
	huma.Register(h.api, huma.Operation{OperationID: "list-discounts", Method: http.MethodGet, Path: "/api/v1/discounts", Summary: "Скидки", Tags: tags}, h.listDiscounts)
	huma.Register(h.api, huma.Operation{OperationID: "create-discount", Method: http.MethodPost, Path: "/api/v1/discounts", Summary: "Создать скидку", Tags: tags}, h.createDiscount)
	huma.Register(h.api, huma.Operation{OperationID: "update-discount", Method: http.MethodPut, Path: "/api/v1/discounts/{id}", Summary: "Изменить скидку", Tags: tags}, h.updateDiscount)
	huma.Register(h.api, huma.Operation{OperationID: "enable-discount", Method: http.MethodPost, Path: "/api/v1/discounts/{id}/enabled", Summary: "Включить или выключить скидку", Tags: tags}, h.enableDiscount)
	huma.Register(h.api, huma.Operation{OperationID: "delete-discount", Method: http.MethodDelete, Path: "/api/v1/discounts/{id}", Summary: "Удалить скидку", Tags: tags}, h.deleteDiscount)
	huma.Register(h.api, huma.Operation{OperationID: "list-discount-redemptions", Method: http.MethodGet, Path: "/api/v1/discounts/redemptions", Summary: "История скидок", Tags: tags}, h.listDiscountRedemptions)
}

// discountView is a discount as the admin's page shows it.
func (h *handlers) discountView(ctx context.Context, d db.Discount) DiscountView {
	var ids []int64
	rows, err := h.d.Store.Q.ListDiscountTariffs(ctx, d.ID)
	if err == nil {
		ids = rows
	}
	if ids == nil {
		ids = []int64{}
	}
	days := discounts.Days(d.NotifyDays)
	if days == nil {
		days = []int{}
	}
	audience := d.NotifyAudience
	if audience == "" {
		audience = discounts.AudienceBuyers
	}
	return DiscountView{
		ID: d.ID, Name: d.Name, Description: d.Description, Type: d.Type, Value: d.Value, Currency: d.Currency,
		TariffIDs: ids, StartsAt: time.Unix(d.StartsAt, 0), EndsAt: time.Unix(d.EndsAt, 0),
		MaxUses: func() *int64 {
			if d.MaxUses.Valid {
				v := d.MaxUses.Int64
				return &v
			}
			return nil
		}(), UsedCount: d.UsedCount, MinOrder: d.MinOrder, MaxDiscount: d.MaxDiscount,
		NotifyEnabled: d.NotifyEnabled != 0, NotifyDays: days, NotifyAudience: audience, NotifyText: d.NotifyText,
		Enabled: d.Enabled != 0, Status: discounts.Status(d, h.d.Now()), CreatedAt: time.Unix(d.CreatedAt, 0),
		CreatedBy: func() *int64 {
			if d.CreatedBy.Valid {
				v := d.CreatedBy.Int64
				return &v
			}
			return nil
		}(),
		Recipients: h.discountRecipients(ctx, d),
	}
}

// discountRecipients is how many accounts the mailing of a discount goes to now.
func (h *handlers) discountRecipients(ctx context.Context, d db.Discount) int64 {
	if d.NotifyAudience == discounts.AudienceSubscribers {
		n, err := h.d.Store.Q.CountDiscountSubscribers(ctx, d.ID)
		if err != nil {
			return 0
		}
		return n
	}
	n, err := h.d.Store.Q.CountDiscountBuyers(ctx, d.ID)
	if err != nil {
		return 0
	}
	return n
}

// validateDiscountBody checks what the admin sent: the numbers are in the currency's
// smallest units, and the mailing's days are the days the bot may say a thing.
func validateDiscountBody(in DiscountBody, now time.Time) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("bad_name")
	}
	switch in.Type {
	case discounts.TypePercent:
		if in.Value < 1 || in.Value > 100 {
			return errors.New("bad_percent")
		}
		if in.Currency != "" && in.Currency != "RUB" && in.Currency != "XTR" {
			return errors.New("bad_currency")
		}
	case discounts.TypeFixed:
		if in.Currency != "RUB" && in.Currency != "XTR" {
			return errors.New("bad_currency")
		}
	default:
		return errors.New("bad_type")
	}
	if in.StartsAt <= 0 || in.EndsAt <= 0 {
		return errors.New("bad_dates")
	}
	if in.StartsAt >= in.EndsAt {
		return errors.New("bad_dates")
	}
	if in.EndsAt <= now.Unix() {
		return errors.New("ends_at_past")
	}
	if in.MinOrder < 0 || in.MaxDiscount < 0 {
		return errors.New("bad_value")
	}
	if in.MaxUses != nil && *in.MaxUses < 1 {
		return errors.New("bad_value")
	}
	if in.NotifyAudience == "" {
		return errors.New("bad_audience")
	}
	if in.NotifyAudience != discounts.AudienceBuyers && in.NotifyAudience != discounts.AudienceSubscribers {
		return errors.New("bad_audience")
	}
	for _, d := range in.NotifyDays {
		if d < 1 || d > 30 {
			return errors.New("bad_notify_days")
		}
	}
	for _, id := range in.TariffIDs {
		if id <= 0 {
			return errors.New("bad_tariff_ids")
		}
	}
	return nil
}

// discountInput is the row a discount is saved as.
func (h *handlers) discountInput(in DiscountBody, createdBy sql.NullInt64) (db.CreateDiscountParams, error) {
	if err := validateDiscountBody(in, h.d.Now()); err != nil {
		return db.CreateDiscountParams{}, err
	}
	if in.Type != discounts.TypeFixed {
		in.Currency = ""
	}
	if in.NotifyAudience == "" {
		in.NotifyAudience = discounts.AudienceBuyers
	}
	days := in.NotifyDays
	if len(days) == 0 {
		days = discounts.DefaultDays
	}
	days = slices.Compact(slices.Sorted(slices.Values(days)))
	raw, _ := json.Marshal(days)
	return db.CreateDiscountParams{
		Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), Type: in.Type, Value: in.Value,
		Currency: in.Currency, StartsAt: in.StartsAt, EndsAt: in.EndsAt, MaxUses: domain.NullInt(in.MaxUses),
		MinOrder: in.MinOrder, MaxDiscount: in.MaxDiscount, NotifyEnabled: domain.Flag(in.NotifyEnabled),
		NotifyDays: string(raw), NotifyAudience: in.NotifyAudience, NotifyText: strings.TrimSpace(in.NotifyText),
		Enabled: domain.Flag(in.Enabled), CreatedAt: h.d.Now().Unix(), CreatedBy: createdBy,
	}, nil
}

// setDiscountTariffs keeps the tariffs of a discount: none: every tariff on sale.
func (h *handlers) setDiscountTariffs(ctx context.Context, q *db.Queries, discountID int64, ids []int64) error {
	if err := q.SetDiscountTariffs(ctx, discountID); err != nil {
		return err
	}
	for _, id := range ids {
		if err := q.AddDiscountTariff(ctx, db.AddDiscountTariffParams{DiscountID: discountID, TariffID: id}); err != nil {
			return err
		}
	}
	return nil
}

// validateDiscountReferences checks that the tariffs of a discount exist.
func (h *handlers) validateDiscountReferences(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		if _, err := h.d.Store.Q.GetTariff(ctx, id); err != nil {
			return errors.New("bad_tariff_ids")
		}
	}
	return nil
}

func (h *handlers) listDiscounts(ctx context.Context, in *discountListInput) (*discountListOutput, error) {
	xs, err := h.d.Store.Q.ListDiscounts(ctx, db.ListDiscountsParams{Lim: in.Limit, RowOffset: in.Offset})
	if err != nil {
		return nil, err
	}
	n, err := h.d.Store.Q.CountDiscounts(ctx)
	if err != nil {
		return nil, err
	}
	o := &discountListOutput{}
	o.Body.Total = n
	o.Body.Items = make([]DiscountView, 0, len(xs))
	for _, d := range xs {
		o.Body.Items = append(o.Body.Items, h.discountView(ctx, d))
	}
	return o, nil
}

func (h *handlers) createDiscount(ctx context.Context, in *struct{ Body DiscountBody }) (*discountOneOutput, error) {
	if err := h.validateDiscountReferences(ctx, in.Body.TariffIDs); err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	p, err := h.discountInput(in.Body, sql.NullInt64{Int64: sessionOf(ctx).AdminID, Valid: true})
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	var d db.Discount
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateDiscount(ctx, p)
		if err != nil {
			return err
		}
		if err := h.setDiscountTariffs(ctx, q, row.ID, in.Body.TariffIDs); err != nil {
			return err
		}
		d = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "discount.create", "discount", strconv64(d.ID), map[string]any{"name": d.Name})
	return &discountOneOutput{Body: h.discountView(ctx, d)}, nil
}

func (h *handlers) updateDiscount(ctx context.Context, in *struct {
	ID   int64 `path:"id"`
	Body DiscountBody
}) (*discountOneOutput, error) {
	if err := h.validateDiscountReferences(ctx, in.Body.TariffIDs); err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	old, err := h.d.Store.Q.GetDiscount(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	p, err := h.discountInput(in.Body, old.CreatedBy)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	var d db.Discount
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		row, err := q.UpdateDiscount(ctx, db.UpdateDiscountParams{
			Name: p.Name, Description: p.Description, Type: p.Type, Value: p.Value, Currency: p.Currency,
			StartsAt: p.StartsAt, EndsAt: p.EndsAt, MaxUses: p.MaxUses, MinOrder: p.MinOrder, MaxDiscount: p.MaxDiscount,
			NotifyEnabled: p.NotifyEnabled, NotifyDays: p.NotifyDays, NotifyAudience: p.NotifyAudience, NotifyText: p.NotifyText,
			Enabled: p.Enabled, ID: in.ID,
		})
		if err != nil {
			return err
		}
		if err := h.setDiscountTariffs(ctx, q, row.ID, in.Body.TariffIDs); err != nil {
			return err
		}
		d = row
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "discount.update", "discount", strconv64(in.ID), nil)
	return &discountOneOutput{Body: h.discountView(ctx, d)}, nil
}

func (h *handlers) enableDiscount(ctx context.Context, in *struct {
	ID   int64 `path:"id"`
	Body struct {
		Enabled bool `json:"enabled"`
	}
}) (*discountOneOutput, error) {
	n, err := h.d.Store.Q.SetDiscountEnabled(ctx, db.SetDiscountEnabledParams{ID: in.ID, Enabled: domain.Flag(in.Body.Enabled)})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	d, err := h.d.Store.Q.GetDiscount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "discount.enabled", "discount", strconv64(in.ID), map[string]any{"enabled": in.Body.Enabled})
	return &discountOneOutput{Body: h.discountView(ctx, d)}, nil
}

func (h *handlers) deleteDiscount(ctx context.Context, in *struct {
	ID int64 `path:"id"`
}) (*struct{}, error) {
	n, err := h.d.Store.Q.DeleteDiscount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "discount.delete", "discount", strconv64(in.ID), nil)
	return &struct{}{}, nil
}

func (h *handlers) listDiscountRedemptions(ctx context.Context, in *discountRedemptionsInput) (*discountRedemptionsOutput, error) {
	xs, err := h.d.Store.Q.ListDiscountRedemptionsWithDiscount(ctx, db.ListDiscountRedemptionsWithDiscountParams{Lim: in.Limit, RowOffset: in.Offset})
	if err != nil {
		return nil, err
	}
	n, err := h.d.Store.Q.CountDiscountRedemptions(ctx)
	if err != nil {
		return nil, err
	}
	o := &discountRedemptionsOutput{}
	o.Body.Total = n
	o.Body.Items = make([]DiscountRedemptionView, 0, len(xs))
	for _, r := range xs {
		o.Body.Items = append(o.Body.Items, DiscountRedemptionView{
			ID: r.ID, DiscountID: r.DiscountID, DiscountName: r.DiscountName,
			UserID: func() *int64 {
				if r.UserID.Valid {
					v := r.UserID.Int64
					return &v
				}
				return nil
			}(),
			TgID: r.TgID,
			PaymentID: func() *int64 {
				if r.PaymentID.Valid {
					v := r.PaymentID.Int64
					return &v
				}
				return nil
			}(),
			Status: r.Status, RedeemedAt: time.Unix(r.RedeemedAt, 0), DiscountAmount: r.DiscountAmount,
			OriginalAmount: r.OriginalAmount, FinalAmount: r.FinalAmount, Currency: r.Currency,
		})
	}
	return o, nil
}
