package agents

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/campaigns"
	"mikan/internal/panel/store/db"
)

// tooMany answers a refused login with the moment to try again.
func tooMany(wait time.Duration) error {
	secs := int(wait.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return huma.ErrorWithHeaders(huma.Error429TooManyRequests("rate_limited"), http.Header{"Retry-After": {strconv.Itoa(secs)}})
}

// The panel's views. Money is kopecks of RUB, the one currency the commissions keep.
type SelfView struct {
	ID        int64     `json:"id"`
	Login     string    `json:"login"`
	CreatedAt time.Time `json:"created_at"`
	CSRFToken string    `json:"csrf_token" doc:"Заголовок X-CSRF-Token на изменяющих запросах"`
}

type OverviewBody struct {
	Visits   int64  `json:"visits" doc:"Сколько людей пришло по ссылкам"`
	Bought   int64  `json:"bought" doc:"Сколько из них купили подписку впервые"`
	Renewed  int64  `json:"renewed" doc:"Сколько оплат было продлениями"`
	Revenue  int64  `json:"revenue" doc:"Сумма оплаченного через ссылки, в копейках"`
	Earned   int64  `json:"earned" doc:"Сколько заработано процентом с продаж, в копейках"`
	Paid     int64  `json:"paid" doc:"Сколько уже выдано, в копейках"`
	Owed     int64  `json:"owed" doc:"Осталось к выдаче: заработанное минус выданное, в копейках"`
	Currency string `json:"currency" enum:"RUB"`
}

type LinkView struct {
	ID        int64     `json:"id"`
	Tag       string    `json:"tag"`
	Name      string    `json:"name,omitempty"`
	Enabled   bool      `json:"enabled"`
	Link      string    `json:"link,omitempty" doc:"Ссылка на бота с меткой кампании; пусто, пока бот не подключён"`
	Percent   int64     `json:"percent" doc:"Процент с продаж этой ссылки"`
	Visits    int64     `json:"visits" doc:"Сколько людей пришло"`
	Bought    int64     `json:"bought" doc:"Сколько из них купили впервые"`
	Renewed   int64     `json:"renewed" doc:"Сколько оплат было продлениями"`
	Revenue   int64     `json:"revenue" doc:"Сумма оплат, в копейках"`
	Earned    int64     `json:"earned" doc:"Заработано по этой ссылке, в копейках"`
	CreatedAt time.Time `json:"created_at"`
}

type LinksBody struct {
	Items []LinkView `json:"items"`
	Total int64      `json:"total"`
}

type ReferralView struct {
	ChatID    int64     `json:"chat_id"`
	Name      string    `json:"name,omitempty"`
	Tag       string    `json:"tag"`
	FirstSeen time.Time `json:"first_seen"`
	Bought    int64     `json:"bought" doc:"Сколько раз купил подписку впервые"`
	Renewed   int64     `json:"renewed" doc:"Сколько оплат было продлениями"`
	Spent     int64     `json:"spent" doc:"Сумма оплат человека в копейках (только RUB)"`
}

type ReferralsBody struct {
	Items []ReferralView `json:"items"`
	Total int64          `json:"total"`
}

type PayoutView struct {
	ID        int64     `json:"id"`
	Amount    int64     `json:"amount" doc:"Выдано, в копейках"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type PayoutsBody struct {
	Items []PayoutView `json:"items"`
	Total int64        `json:"total"`
}

func (h *apiH) register() {
	tags := []string{"agents"}
	public := map[string]any{"public": true}
	huma.Register(h.api, huma.Operation{OperationID: "agent-login", Method: http.MethodPost, Path: "/api/agent/login", Summary: "Вход рекламного агента", Tags: tags, Metadata: public, Security: []map[string][]string{{}}}, h.login)
	huma.Register(h.api, huma.Operation{OperationID: "agent-logout", Method: http.MethodPost, Path: "/api/agent/logout", Summary: "Выход", Tags: tags, DefaultStatus: http.StatusNoContent}, h.logout)
	huma.Register(h.api, huma.Operation{OperationID: "agent-me", Method: http.MethodGet, Path: "/api/agent/me", Summary: "Текущий агент", Tags: tags}, h.me)
	huma.Register(h.api, huma.Operation{OperationID: "agent-overview", Method: http.MethodGet, Path: "/api/agent/overview", Summary: "Сводка за всё время", Tags: tags}, h.overview)
	huma.Register(h.api, huma.Operation{OperationID: "agent-links", Method: http.MethodGet, Path: "/api/agent/links", Summary: "Ссылки агента", Tags: tags}, h.links)
	huma.Register(h.api, huma.Operation{OperationID: "agent-referrals", Method: http.MethodGet, Path: "/api/agent/referrals", Summary: "Кто пришёл по ссылкам", Tags: tags}, h.referrals)
	huma.Register(h.api, huma.Operation{OperationID: "agent-payouts", Method: http.MethodGet, Path: "/api/agent/payouts", Summary: "Выплаты", Tags: tags}, h.payouts)
}

type agentLoginInput struct {
	Body struct {
		Login    string `json:"login" minLength:"1" maxLength:"64"`
		Password string `json:"password" minLength:"1" maxLength:"256"`
	}
}

type loginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      SelfView
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type meOutput struct{ Body SelfView }

type overviewOutput struct{ Body OverviewBody }

// viewSelf is who the session belongs to, with the fresh CSRF token of it.
func (h *apiH) viewSelf(ctx context.Context, sess db.AgentSession) (SelfView, error) {
	a, err := h.d.Service.Store.Q.GetAgent(ctx, sess.AgentID)
	if err != nil {
		return SelfView{}, err
	}
	return SelfView{ID: a.ID, Login: a.Login, CreatedAt: time.Unix(a.CreatedAt, 0).UTC(), CSRFToken: sess.CsrfToken}, nil
}

func (h *apiH) login(ctx context.Context, in *agentLoginInput) (*loginOutput, error) {
	c := clientOf(ctx)
	token, sess, err := h.d.Service.Login(ctx, in.Body.Login, in.Body.Password, c.IP, c.UserAgent)
	if err != nil {
		var w *WaitError
		if errors.As(err, &w) {
			return nil, tooMany(w.Wait)
		}
		if errors.Is(err, ErrCredentials) {
			return nil, huma.Error401Unauthorized("agent_credentials")
		}
		return nil, err
	}
	self, err := h.viewSelf(ctx, sess)
	if err != nil {
		return nil, err
	}
	return &loginOutput{SetCookie: agentCookie(token, auth.MaxTTL), Body: self}, nil
}

func (h *apiH) logout(ctx context.Context, _ *struct{}) (*logoutOutput, error) {
	sess := sessionOf(ctx)
	if err := h.d.Service.Logout(ctx, sess.IDHash); err != nil {
		return nil, err
	}
	return &logoutOutput{SetCookie: agentCookie("", 0)}, nil
}

func (h *apiH) me(ctx context.Context, _ *struct{}) (*meOutput, error) {
	self, err := h.viewSelf(ctx, sessionOf(ctx))
	if err != nil {
		return nil, err
	}
	return &meOutput{Body: self}, nil
}

func (h *apiH) overview(ctx context.Context, _ *struct{}) (*overviewOutput, error) {
	st, err := h.d.Service.StatsOf(ctx, sessionOf(ctx).AgentID)
	if err != nil {
		return nil, err
	}
	return &overviewOutput{Body: OverviewBody{
		Visits: st.Visits, Bought: st.Bought, Renewed: st.Renewed, Revenue: st.Revenue,
		Earned: st.Earned, Paid: st.Paid, Owed: st.Owed(), Currency: Currency,
	}}, nil
}

type agentListInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

func (h *apiH) botLink(tag string) string {
	if h.d.BotUsername == nil {
		return ""
	}
	if name := h.d.BotUsername(); name != "" {
		return campaigns.Link(name, tag)
	}
	return ""
}

func (h *apiH) links(ctx context.Context, in *agentListInput) (*LinksBody, error) {
	rows, total, err := h.d.Service.Links(ctx, sessionOf(ctx).AgentID, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	out := &LinksBody{Total: total, Items: make([]LinkView, 0, len(rows))}
	for _, l := range rows {
		out.Items = append(out.Items, LinkView{
			ID: l.ID, Tag: l.Tag, Name: l.Name, Enabled: l.Enabled, Link: h.botLink(l.Tag), Percent: l.Percent,
			Visits: l.Visits, Bought: l.Bought, Renewed: l.Renewed, Revenue: l.Rub, Earned: l.Earned,
			CreatedAt: l.CreatedAt.UTC(),
		})
	}
	return out, nil
}

func (h *apiH) referrals(ctx context.Context, in *agentListInput) (*ReferralsBody, error) {
	rows, total, err := h.d.Service.Referrals(ctx, sessionOf(ctx).AgentID, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	out := &ReferralsBody{Total: total, Items: make([]ReferralView, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, ReferralView{
			ChatID: r.ChatID, Name: r.Name, Tag: r.Tag, FirstSeen: r.FirstSeen.UTC(),
			Bought: r.Bought, Renewed: r.Renewed, Spent: r.Rub,
		})
	}
	return out, nil
}

func (h *apiH) payouts(ctx context.Context, in *agentListInput) (*PayoutsBody, error) {
	rows, total, err := h.d.Service.Payouts(ctx, sessionOf(ctx).AgentID, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	out := &PayoutsBody{Total: total, Items: make([]PayoutView, 0, len(rows))}
	for _, p := range rows {
		out.Items = append(out.Items, PayoutView{ID: p.ID, Amount: p.Amount, Note: p.Note, CreatedAt: time.Unix(p.CreatedAt, 0).UTC()})
	}
	return out, nil
}
