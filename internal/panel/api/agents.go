package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/agents"
	"mikan/internal/panel/settings"
)

// AgentView is one advertising agent as the admin panel shows it: the login and note the
// admin made them with, whether they may log in, and what they have brought, earned and
// been paid.
type AgentView struct {
	ID        int64     `json:"id"`
	Login     string    `json:"login"`
	Note      string    `json:"note,omitempty"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	TgID      *int64    `json:"tg_id" doc:"Телеграм-аккаунт самого агента; null — не указан. Его переходы по своим ссылкам считаются его юзом, а не пришедшими"`
	Links     int64     `json:"links" doc:"Сколько ссылок-кампаний закреплено за агентом"`
	Visits    int64     `json:"visits" doc:"Сколько людей пришло по ссылкам агента"`
	Bought    int64     `json:"bought" doc:"Сколько из них купили подписку впервые"`
	Renewed   int64     `json:"renewed" doc:"Сколько оплат было продлениями"`
	Revenue   int64     `json:"revenue" doc:"Сумма оплаченного через ссылки агента, в копейках"`
	Earned    int64     `json:"earned" doc:"Сколько агент заработал процентом с продаж, в копейках"`
	Paid      int64     `json:"paid" doc:"Сколько агенту уже выдано, в копейках"`
	Owed      int64     `json:"owed" doc:"Сколько агенту осталось выдать, в копейках"`
	Usages    int64     `json:"usages" doc:"Сколько раз сам агент заходил по своим ссылкам; к пришедшим не относится"`
	Currency  string    `json:"currency" enum:"RUB"`
}

type AgentsStatsView struct {
	Agents  int64 `json:"agents"`
	Visits  int64 `json:"visits"`
	Bought  int64 `json:"bought"`
	Renewed int64 `json:"renewed"`
	Revenue int64 `json:"revenue"`
	Earned  int64 `json:"earned"`
	Paid    int64 `json:"paid"`
	Owed    int64 `json:"owed"`
	Usages  int64 `json:"usages"`
}

type agentsOutput struct {
	Body struct {
		Items []AgentView     `json:"items"`
		Total int64           `json:"total"`
		Stats AgentsStatsView `json:"stats"`
	}
}

type agentListInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

type createAgentInput struct {
	Body struct {
		Login    string `json:"login" minLength:"3" maxLength:"64" doc:"Латиница, цифры, точка, дефис и подчёркивание; регистр не важен"`
		Password string `json:"password" minLength:"12" maxLength:"256" doc:"Выдадите агенту вместе со ссылкой на его панель; не короче 12 символов"`
		Note     string `json:"note" maxLength:"500"`
		TgID     *int64 `json:"tg_id,omitempty" minimum:"1" doc:"Телеграм-аккаунт самого агента: его входы по своим ссылкам не считаются пришедшими"`
	}
}

type agentOneOutput struct{ Body AgentView }

type updateAgentInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Login    string `json:"login" minLength:"3" maxLength:"64"`
		Note     string `json:"note" maxLength:"500"`
		Disabled bool   `json:"disabled" doc:"Выключенный агент не может войти, его числа остаются"`
		TgID     *int64 `json:"tg_id,omitempty" minimum:"1" doc:"Телеграм-аккаунт самого агента; null — убрать"`
		// Password: "" keeps the old one. A change ends the agent's sessions at once.
		Password string `json:"password,omitempty" minLength:"0" maxLength:"256"`
	}
}

type PayoutView struct {
	ID        int64     `json:"id"`
	Amount    int64     `json:"amount" doc:"Выдано, в копейках"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type payoutsBody struct {
	Items []PayoutView `json:"items"`
	Total int64        `json:"total"`
}

type payoutsOutput struct{ Body payoutsBody }

type payoutOneOutput struct{ Body PayoutView }

type recordPayoutInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Amount int64  `json:"amount" minimum:"1" doc:"Сколько выдано, в копейках"`
		Note   string `json:"note" maxLength:"200" doc:"Как и когда передано: «наличными 19.10», скажем"`
	}
}

type agentPayoutsInput struct {
	ID int64 `path:"id"`
}

type agentCampaignsInput struct {
	ID     int64 `path:"id"`
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

// AgentLinkView is one of an agent's links as the admin sees it: the placement that was
// handed to the agent, with what it brought and what the agent earned on it.
type AgentLinkView struct {
	ID        int64     `json:"id"`
	Tag       string    `json:"tag"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Percent   int64     `json:"percent" doc:"Процент агента с продаж этой ссылки"`
	URL       string    `json:"url,omitempty" doc:"Ссылка для раздачи; пусто, пока бот без имени"`
	CreatedAt time.Time `json:"created_at"`
	Visits    int64     `json:"visits"`
	Bought    int64     `json:"bought"`
	Renewed   int64     `json:"renewed"`
	Rub       int64     `json:"rub" doc:"Сумма оплаченного через ссылку, в копейках"`
	Earned    int64     `json:"earned" doc:"Сколько агент заработал на этой ссылке, в копейках"`
}

type agentCampaignsOutput struct {
	Body struct {
		Items []AgentLinkView `json:"items"`
		Total int64           `json:"total"`
	}
}

type agentReferralsInput struct {
	ID     int64 `path:"id"`
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

// AgentReferralView is one person an agent's links brought, as the admin sees it.
type AgentReferralView struct {
	ChatID    int64     `json:"chat_id"`
	Name      string    `json:"name"`
	Tag       string    `json:"tag" doc:"Ссылка, по которой человек пришёл"`
	FirstSeen time.Time `json:"first_seen"`
	Bought    int64     `json:"bought"`
	Renewed   int64     `json:"renewed"`
	Rub       int64     `json:"rub"`
}

type agentReferralsOutput struct {
	Body struct {
		Items []AgentReferralView `json:"items"`
		Total int64               `json:"total"`
	}
}

type deleteAgentReferralInput struct {
	ID     int64 `path:"id"`
	ChatID int64 `path:"chat_id"`
}

// agentPanelOutput is where the agents' panel lives; URL is the panel's own address with
// the path attached, ready to hand to the agent.
type agentPanelOutput struct {
	Body struct {
		Path string `json:"path" doc:"Секретный путь панели агентов; пусто — панель выключена"`
		URL  string `json:"url,omitempty" doc:"Полный адрес панели; тоже пустой, пока пути нет"`
	}
}

type setAgentPanelInput struct {
	Body struct {
		Path string `json:"path" maxLength:"64" doc:"От 8 знаков: латиница, цифры, дефис и подчёркивание; пусто — панель выключена"`
	}
}

func (h *handlers) registerAgents() {
	tags := []string{"agents"}
	huma.Register(h.api, huma.Operation{OperationID: "list-agents", Method: http.MethodGet, Path: "/api/v1/agents", Summary: "Рекламные агенты", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listAgents)
	huma.Register(h.api, huma.Operation{OperationID: "create-agent", Method: http.MethodPost, Path: "/api/v1/agents", Summary: "Создать рекламного агента", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.createAgent)
	huma.Register(h.api, huma.Operation{OperationID: "agent-panel", Method: http.MethodGet, Path: "/api/v1/agents/panel", Summary: "Адрес панели агентов", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.agentPanel)
	huma.Register(h.api, huma.Operation{OperationID: "set-agent-panel", Method: http.MethodPut, Path: "/api/v1/agents/panel", Summary: "Задать путь панели агентов", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.setAgentPanel)
	huma.Register(h.api, huma.Operation{OperationID: "update-agent", Method: http.MethodPut, Path: "/api/v1/agents/{id}", Summary: "Изменить агента", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.updateAgent)
	huma.Register(h.api, huma.Operation{OperationID: "list-agent-payouts", Method: http.MethodGet, Path: "/api/v1/agents/{id}/payouts", Summary: "Выплаты агенту", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listAgentPayouts)
	huma.Register(h.api, huma.Operation{OperationID: "record-agent-payout", Method: http.MethodPost, Path: "/api/v1/agents/{id}/payouts", Summary: "Отметить выплату агенту", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.recordAgentPayout)
	huma.Register(h.api, huma.Operation{OperationID: "list-agent-campaigns", Method: http.MethodGet, Path: "/api/v1/agents/{id}/campaigns", Summary: "Ссылки агента", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listAgentCampaigns)
	huma.Register(h.api, huma.Operation{OperationID: "list-agent-referrals", Method: http.MethodGet, Path: "/api/v1/agents/{id}/referrals", Summary: "Кто пришёл по ссылкам агента", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listAgentReferrals)
	huma.Register(h.api, huma.Operation{OperationID: "delete-agent-referral", Method: http.MethodDelete, Path: "/api/v1/agents/{id}/referrals/{chat_id}", Summary: "Убрать человека из пришедших", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.deleteAgentReferral)
}

func viewAgent(a agents.Agent) AgentView {
	var tg *int64
	if a.TgID.Valid {
		tg = &a.TgID.Int64
	}
	return AgentView{
		ID: a.ID, Login: a.Login, Note: a.Note, Disabled: a.Disabled != 0, CreatedAt: time.Unix(a.CreatedAt, 0).UTC(),
		TgID: tg, Links: a.Links, Visits: a.Visits, Bought: a.Bought, Renewed: a.Renewed,
		Revenue: a.Revenue, Earned: a.Earned, Paid: a.Paid, Owed: a.Owed(), Usages: a.Usages, Currency: agents.Currency,
	}
}

// agentErr maps a service refusal to its API answer.
func agentErr(err error) error {
	switch {
	case errors.Is(err, agents.ErrLogin):
		return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.login", Message: "agent_login_invalid"})
	case errors.Is(err, agents.ErrPassword):
		return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.password", Message: "agent_password_invalid"})
	case errors.Is(err, agents.ErrLoginTaken):
		return huma.Error409Conflict("agent_login_taken")
	case errors.Is(err, agents.ErrTgTaken):
		return huma.Error409Conflict("agent_tg_taken")
	case errors.Is(err, agents.ErrNotFound):
		return huma.Error404NotFound("agent_not_found")
	case errors.Is(err, agents.ErrPayout):
		return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.amount", Message: "agent_payout_amount"})
	}
	return err
}

func (h *handlers) listAgents(ctx context.Context, in *agentListInput) (*agentsOutput, error) {
	if h.d.Agents == nil {
		return nil, huma.Error503ServiceUnavailable("agents_unavailable")
	}
	rows, total, err := h.d.Agents.List(ctx, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	sum, err := h.d.Agents.Summary(ctx)
	if err != nil {
		return nil, err
	}
	out := &agentsOutput{}
	out.Body.Total = total
	out.Body.Stats = AgentsStatsView{
		Agents: total, Visits: sum.Visits, Bought: sum.Bought, Renewed: sum.Renewed,
		Revenue: sum.Revenue, Earned: sum.Earned, Paid: sum.Paid, Owed: sum.Owed(), Usages: sum.Usages,
	}
	out.Body.Items = make([]AgentView, 0, len(rows))
	for _, a := range rows {
		out.Body.Items = append(out.Body.Items, viewAgent(a))
	}
	return out, nil
}

func (h *handlers) createAgent(ctx context.Context, in *createAgentInput) (*agentOneOutput, error) {
	if h.d.Agents == nil {
		return nil, huma.Error503ServiceUnavailable("agents_unavailable")
	}
	a, err := h.d.Agents.Create(ctx, in.Body.Login, in.Body.Password, in.Body.Note, in.Body.TgID)
	if err := agentErr(err); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "agent.create", "agent", a.Login, nil)
	out := &agentOneOutput{}
	out.Body = viewAgent(agents.Agent{CampaignAgent: a})
	return out, nil
}

func (h *handlers) updateAgent(ctx context.Context, in *updateAgentInput) (*agentOneOutput, error) {
	if h.d.Agents == nil {
		return nil, huma.Error503ServiceUnavailable("agents_unavailable")
	}
	a, err := h.d.Agents.Update(ctx, in.ID, in.Body.Login, in.Body.Note, in.Body.Disabled, in.Body.TgID)
	if err := agentErr(err); err != nil {
		return nil, err
	}
	if in.Body.Password != "" {
		if err := agentErr(h.d.Agents.SetPassword(ctx, in.ID, in.Body.Password)); err != nil {
			return nil, err
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "agent.password", "agent", a.Login, nil)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "agent.update", "agent", a.Login, map[string]any{"disabled": in.Body.Disabled})
	out := &agentOneOutput{}
	out.Body = viewAgent(agents.Agent{CampaignAgent: a})
	if st, err := h.d.Agents.StatsOf(ctx, a.ID); err == nil {
		out.Body.Visits, out.Body.Bought, out.Body.Renewed, out.Body.Revenue, out.Body.Earned, out.Body.Paid, out.Body.Owed, out.Body.Usages = st.Visits, st.Bought, st.Renewed, st.Revenue, st.Earned, st.Paid, st.Owed(), st.Usages
	}
	return out, nil
}

func (h *handlers) listAgentPayouts(ctx context.Context, in *agentPayoutsInput) (*payoutsOutput, error) {
	if h.d.Agents == nil {
		return nil, huma.Error503ServiceUnavailable("agents_unavailable")
	}
	rows, total, err := h.d.Agents.Payouts(ctx, in.ID, 100, 0)
	if err := agentErr(err); err != nil {
		return nil, err
	}
	out := &payoutsOutput{Body: payoutsBody{Total: total, Items: make([]PayoutView, 0, len(rows))}}
	for _, p := range rows {
		out.Body.Items = append(out.Body.Items, PayoutView{ID: p.ID, Amount: p.Amount, Note: p.Note, CreatedAt: time.Unix(p.CreatedAt, 0).UTC()})
	}
	return out, nil
}

func (h *handlers) recordAgentPayout(ctx context.Context, in *recordPayoutInput) (*payoutOneOutput, error) {
	if h.d.Agents == nil {
		return nil, huma.Error503ServiceUnavailable("agents_unavailable")
	}
	p, err := h.d.Agents.Payout(ctx, in.ID, in.Body.Amount, in.Body.Note)
	if err := agentErr(err); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "agent.payout", "agent", "", map[string]any{"agent_id": in.ID, "amount": in.Body.Amount})
	out := &payoutOneOutput{}
	out.Body = PayoutView{ID: p.ID, Amount: p.Amount, Note: p.Note, CreatedAt: time.Unix(p.CreatedAt, 0).UTC()}
	return out, nil
}

func (h *handlers) listAgentCampaigns(ctx context.Context, in *agentCampaignsInput) (*agentCampaignsOutput, error) {
	if h.d.Agents == nil {
		return nil, huma.Error503ServiceUnavailable("agents_unavailable")
	}
	links, total, err := h.d.Agents.Links(ctx, in.ID, in.Limit, in.Offset)
	if err := agentErr(err); err != nil {
		return nil, err
	}
	out := &agentCampaignsOutput{}
	out.Body.Total = total
	out.Body.Items = make([]AgentLinkView, 0, len(links))
	for _, l := range links {
		out.Body.Items = append(out.Body.Items, AgentLinkView{
			ID: l.ID, Tag: l.Tag, Name: l.Name, Enabled: l.Enabled, Percent: l.Percent,
			URL: h.botLink(l.Tag), CreatedAt: l.CreatedAt,
			Visits: l.Visits, Bought: l.Bought, Renewed: l.Renewed, Rub: l.Rub, Earned: l.Earned,
		})
	}
	return out, nil
}

func (h *handlers) listAgentReferrals(ctx context.Context, in *agentReferralsInput) (*agentReferralsOutput, error) {
	if h.d.Agents == nil {
		return nil, huma.Error503ServiceUnavailable("agents_unavailable")
	}
	refs, total, err := h.d.Agents.Referrals(ctx, in.ID, in.Limit, in.Offset)
	if err := agentErr(err); err != nil {
		return nil, err
	}
	out := &agentReferralsOutput{}
	out.Body.Total = total
	out.Body.Items = make([]AgentReferralView, 0, len(refs))
	for _, r := range refs {
		out.Body.Items = append(out.Body.Items, AgentReferralView{
			ChatID: r.ChatID, Name: r.Name, Tag: r.Tag, FirstSeen: r.FirstSeen,
			Bought: r.Bought, Renewed: r.Renewed, Rub: r.Rub,
		})
	}
	return out, nil
}

func (h *handlers) deleteAgentReferral(ctx context.Context, in *deleteAgentReferralInput) (*struct{}, error) {
	if h.d.Agents == nil {
		return nil, huma.Error503ServiceUnavailable("agents_unavailable")
	}
	if err := h.d.Agents.RemoveReferral(ctx, in.ID, in.ChatID); err != nil {
		return nil, agentErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "agent.referral_remove", "agent", "", map[string]any{"agent_id": in.ID, "chat_id": in.ChatID})
	return &struct{}{}, nil
}

// agentPanel is where the agents' panel is served from. The URL is the panel's own
// address with the path attached, ready to hand over; both empty while the panel is off.
func (h *handlers) agentPanel(ctx context.Context, _ *struct{}) (*agentPanelOutput, error) {
	p, err := h.d.Settings.String(ctx, settings.KeyAgentPath)
	if err != nil {
		return nil, err
	}
	return h.agentPanelOut(ctx, p), nil
}

func (h *handlers) setAgentPanel(ctx context.Context, in *setAgentPanelInput) (*agentPanelOutput, error) {
	if err := h.checkAgentPath(ctx, in.Body.Path); err != nil {
		return nil, err
	}
	if err := settings.Set(ctx, h.d.Settings, settings.KeyAgentPath, in.Body.Path); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "agent.panel_path", "", "", map[string]any{"path": in.Body.Path})
	return h.agentPanelOut(ctx, in.Body.Path), nil
}

// agentPanelOut spells the path and, when the panel has an address at all, its URL.
func (h *handlers) agentPanelOut(ctx context.Context, p string) *agentPanelOutput {
	out := &agentPanelOutput{}
	out.Body.Path = p
	if p != "" {
		if ep, err := h.d.Settings.Endpoint(ctx); err == nil {
			out.Body.URL = ep.URL() + "/" + p + "/"
		}
	}
	return out
}

// checkAgentPath holds the agent path to one segment of safe letters and apart from the
// panel's other secret paths; "" switches the panel off and is always fine.
func (h *handlers) checkAgentPath(ctx context.Context, p string) error {
	if p == "" {
		return nil
	}
	bad := func() error {
		return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.path", Message: "agent_path_invalid"})
	}
	if len(p) < 8 || len(p) > 64 {
		return bad()
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return bad()
		}
	}
	paths, err := h.d.Settings.Paths(ctx)
	if err != nil {
		return err
	}
	if p == paths.Admin || p == paths.Sub || p == paths.Legacy {
		return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.path", Message: "agent_path_taken"})
	}
	return nil
}
