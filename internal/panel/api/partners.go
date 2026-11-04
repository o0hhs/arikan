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

	"mikan/internal/panel/secure"
	"mikan/internal/panel/store/db"
)

// Partners and their bots: the franchise's books. A bot's code is what its subscribers
// come to the panel's bot with; a paid order of such an account credits the partner.

// PartnerBotView is one bot of a partner as the page shows it.
type PartnerBotView struct {
	ID                int64     `json:"id"`
	Code              string    `json:"code" doc:"Код в /start partner_<code>, с которым приходят подписчики"`
	BotUsername       string    `json:"bot_username"`
	BotToken          string    `json:"bot_token,omitempty" doc:"Токен бота партнёра; пусто у оператора поддержки"`
	CommissionPercent int64     `json:"commission_percent" doc:"Процент с оплат подписчиков"`
	BalanceKop        int64     `json:"balance_kop" doc:"Баланс партнёра, копейки"`
	TurnoverKop       int64     `json:"turnover_kop" doc:"Начислено всего, копейки"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
}

// PartnerView is a partner with its bots.
type PartnerView struct {
	ID         int64            `json:"id"`
	Name       string           `json:"name"`
	Note       string           `json:"note"`
	OwnerTgID  int64            `json:"owner_tg_id,omitempty" doc:"Свой Telegram-аккаунт партнёра; 0 — не задан"`
	Bots       []PartnerBotView `json:"bots"`
	BalanceKop int64            `json:"balance_kop" doc:"Сумма балансов ботов, копейки"`
}

type partnerListOutput struct{ Body []PartnerView }
type partnerOneOutput struct{ Body PartnerView }

type partnerBody struct {
	Name string `json:"name" minLength:"1" maxLength:"120"`
	Note string `json:"note" maxLength:"500"`
	// OwnerTgID is the partner's own Telegram account: its code pays it nothing. 0: none.
	OwnerTgID int64 `json:"owner_tg_id,omitempty" minimum:"0" doc:"Свой Telegram-аккаунт партнёра: по его же коду комиссия не начисляется; 0 — не задан"`
}

type partnerBotBody struct {
	BotUsername       string `json:"bot_username" maxLength:"64"`
	BotToken          string `json:"bot_token" maxLength:"200" doc:"Токен бота партнёра; пусто — оставить прежний"`
	CommissionPercent int64  `json:"commission_percent" minimum:"0" maximum:"100"`
	Enabled           bool   `json:"enabled"`
	Code              string `json:"code,omitempty" maxLength:"64" doc:"Свой код из латиницы, цифр, _ и -; пусто — выданный панелью"`
}

func (h *handlers) registerPartners() {
	tags := []string{"partners"}
	// The franchise's books: a bot's token is a credential and the commission decides where
	// money goes, so neither a "full" API key nor a support operator short of the extended
	// mode may touch them (sessionOnly, like the agents' page).
	huma.Register(h.api, huma.Operation{OperationID: "list-partners", Method: http.MethodGet, Path: "/api/v1/partners", Summary: "Партнёры", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listPartners)
	huma.Register(h.api, huma.Operation{OperationID: "create-partner", Method: http.MethodPost, Path: "/api/v1/partners", Summary: "Добавить партнёра", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.createPartner)
	huma.Register(h.api, huma.Operation{OperationID: "update-partner", Method: http.MethodPut, Path: "/api/v1/partners/{id}", Summary: "Изменить партнёра", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.updatePartner)
	huma.Register(h.api, huma.Operation{OperationID: "delete-partner", Method: http.MethodDelete, Path: "/api/v1/partners/{id}", Summary: "Удалить партнёра", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt, DefaultStatus: http.StatusNoContent}, h.deletePartner)
	huma.Register(h.api, huma.Operation{OperationID: "create-partner-bot", Method: http.MethodPost, Path: "/api/v1/partners/{id}/bots", Summary: "Добавить бота партнёра", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.createPartnerBot)
	huma.Register(h.api, huma.Operation{OperationID: "update-partner-bot", Method: http.MethodPut, Path: "/api/v1/partners/bots/{bot}", Summary: "Изменить бота партнёра", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.updatePartnerBot)
	huma.Register(h.api, huma.Operation{OperationID: "delete-partner-bot", Method: http.MethodDelete, Path: "/api/v1/partners/bots/{bot}", Summary: "Удалить бота партнёра", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt, DefaultStatus: http.StatusNoContent}, h.deletePartnerBot)
}

// partnerBotView is one bot as the page shows it. Its token is a credential: only a caller
// that hidesSecrets lets through gets it, so a support operator (even in the extended mode)
// is not handed the partner's bot token by a mutation's answer.
func (h *handlers) partnerBotView(ctx context.Context, b db.PartnerBot, turnover int64) PartnerBotView {
	v := PartnerBotView{ID: b.ID, Code: b.Code, BotUsername: b.BotUsername, CommissionPercent: int64(b.CommissionPercent),
		BalanceKop: b.BalanceKop, TurnoverKop: turnover, Enabled: b.Enabled, CreatedAt: time.Unix(b.CreatedAt, 0)}
	if !hidesSecrets(ctx) {
		v.BotToken = b.BotToken
	}
	return v
}

// validPartnerCode: a code travels in a Telegram deep link (/start partner_<code>), so it is
// the characters a start parameter takes, in a length the bot reads back.
func validPartnerCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (h *handlers) listPartners(ctx context.Context, _ *struct{}) (*partnerListOutput, error) {
	ps, err := h.d.Store.Q.ListPartners(ctx)
	if err != nil {
		return nil, err
	}
	bots, err := h.d.Store.Q.ListPartnerBots(ctx)
	if err != nil {
		return nil, err
	}
	byPartner := map[int64][]db.PartnerBot{}
	for _, b := range bots {
		byPartner[b.PartnerID] = append(byPartner[b.PartnerID], b)
	}
	out := &partnerListOutput{Body: make([]PartnerView, 0, len(ps))}
	for _, p := range ps {
		v := PartnerView{ID: p.ID, Name: p.Name, Note: p.Note, OwnerTgID: p.OwnerTgID, Bots: []PartnerBotView{}}
		for _, b := range byPartner[p.ID] {
			turnover, err := h.d.Store.Q.PartnerTurnover(ctx, b.ID)
			if err != nil {
				return nil, err
			}
			v.Bots = append(v.Bots, h.partnerBotView(ctx, b, turnover))
			v.BalanceKop += b.BalanceKop
		}
		out.Body = append(out.Body, v)
	}
	return out, nil
}

func (h *handlers) createPartner(ctx context.Context, in *struct{ Body partnerBody }) (*partnerOneOutput, error) {
	p, err := h.d.Store.Q.CreatePartner(ctx, db.CreatePartnerParams{Name: strings.TrimSpace(in.Body.Name), Note: strings.TrimSpace(in.Body.Note), OwnerTgID: in.Body.OwnerTgID, CreatedAt: h.d.Now().Unix()})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "partner.create", "partner", strconv.FormatInt(p.ID, 10), map[string]any{"name": p.Name})
	return &partnerOneOutput{Body: PartnerView{ID: p.ID, Name: p.Name, Note: p.Note, OwnerTgID: p.OwnerTgID, Bots: []PartnerBotView{}}}, nil
}

func (h *handlers) updatePartner(ctx context.Context, in *struct {
	ID   int64 `path:"id"`
	Body partnerBody
}) (*partnerOneOutput, error) {
	p, err := h.d.Store.Q.UpdatePartner(ctx, db.UpdatePartnerParams{ID: in.ID, Name: strings.TrimSpace(in.Body.Name), Note: strings.TrimSpace(in.Body.Note), OwnerTgID: nullableInt(in.Body.OwnerTgID)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "partner.update", "partner", strconv.FormatInt(in.ID, 10), nil)
	return &partnerOneOutput{Body: PartnerView{ID: p.ID, Name: p.Name, Note: p.Note, OwnerTgID: p.OwnerTgID, Bots: []PartnerBotView{}}}, nil
}

func (h *handlers) deletePartner(ctx context.Context, in *userIDInput) (*struct{}, error) {
	n, err := h.d.Store.Q.DeletePartner(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "partner.delete", "partner", strconv.FormatInt(in.ID, 10), nil)
	return &struct{}{}, nil
}

func (h *handlers) createPartnerBot(ctx context.Context, in *struct {
	ID   int64 `path:"id"`
	Body partnerBotBody
}) (*partnerOneOutput, error) {
	if _, err := h.d.Store.Q.GetPartner(ctx, in.ID); errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	} else if err != nil {
		return nil, err
	}
	code := strings.TrimSpace(in.Body.Code)
	if code == "" {
		code = strings.ToLower(secure.Token(8))
	}
	if !validPartnerCode(code) {
		return nil, huma.Error400BadRequest("bad_code")
	}
	b, err := h.d.Store.Q.CreatePartnerBot(ctx, db.CreatePartnerBotParams{PartnerID: in.ID, Code: code, BotUsername: strings.TrimSpace(in.Body.BotUsername),
		BotToken: strings.TrimSpace(in.Body.BotToken), CommissionPercent: int32(in.Body.CommissionPercent), CreatedAt: h.d.Now().Unix()})
	if err != nil {
		return nil, huma.Error400BadRequest("bad_code")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "partner.bot.create", "partner_bot", strconv.FormatInt(b.ID, 10), map[string]any{"partner": in.ID, "code": b.Code})
	return &partnerOneOutput{Body: PartnerView{ID: in.ID, Bots: []PartnerBotView{h.partnerBotView(ctx, b, 0)}}}, nil
}

func (h *handlers) updatePartnerBot(ctx context.Context, in *struct {
	Bot  int64 `path:"bot"`
	Body partnerBotBody
}) (*partnerOneOutput, error) {
	cur, err := h.d.Store.Q.GetPartnerBot(ctx, in.Bot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(in.Body.BotToken)
	if token == "" {
		token = cur.BotToken
	}
	b, err := h.d.Store.Q.UpdatePartnerBot(ctx, db.UpdatePartnerBotParams{ID: in.Bot, BotUsername: strings.TrimSpace(in.Body.BotUsername), BotToken: token,
		CommissionPercent: int32(in.Body.CommissionPercent), Enabled: in.Body.Enabled})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "partner.bot.update", "partner_bot", strconv.FormatInt(in.Bot, 10), nil)
	return &partnerOneOutput{Body: PartnerView{ID: b.PartnerID, Bots: []PartnerBotView{h.partnerBotView(ctx, b, 0)}}}, nil
}

func (h *handlers) deletePartnerBot(ctx context.Context, in *struct {
	Bot int64 `path:"bot"`
}) (*struct{}, error) {
	n, err := h.d.Store.Q.DeletePartnerBot(ctx, in.Bot)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "partner.bot.delete", "partner_bot", strconv.FormatInt(in.Bot, 10), nil)
	return &struct{}{}, nil
}

// nullableInt keeps an omitted owner as it was: 0 drops it, a positive id names the account.
func nullableInt(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: v > 0}
}
