package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/automation"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The automation page: event-driven rules and the journal of their runs. The rules only
// speak Telegram and use the panel's own grants; nothing here runs a command or calls an
// arbitrary address.

type AutomationRuleView struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	Trigger         string          `json:"trigger" enum:"trial.started,user.created,user.granted,user.expiring,user.expired,user.inactive,traffic.90,traffic.100,payment.paid,payment.failed,payment.refunded,trial.not_given,device.bound,device.unbound,balance.topup,discount.ending,node.down,node.up"`
	Conditions      json.RawMessage `json:"conditions" doc:"JSON: tariff_ids, states, sources, days_left_min/max, traffic_used_min/max"`
	Actions         json.RawMessage `json:"actions" doc:"JSON-массив действий: notify_admin, notify_user, extend, grant_traffic, promo, discount"`
	CooldownSeconds int64           `json:"cooldown_seconds"`
	DryRun          bool            `json:"dry_run"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	LastRunAt       *time.Time      `json:"last_run_at,omitempty"`
	RunsCount       int64           `json:"runs_count"`
}

type AutomationRunView struct {
	ID        int64     `json:"id"`
	RuleID    int64     `json:"rule_id"`
	EventID   int64     `json:"event_id"`
	EventType string    `json:"event_type"`
	UserID    *int64    `json:"user_id,omitempty"`
	TgID      *int64    `json:"tg_id,omitempty"`
	Status    string    `json:"status" enum:"ok,skipped,failed"`
	DryRun    bool      `json:"dry_run"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

type automationRulesOutput struct{ Body []AutomationRuleView }
type automationRuleOutput struct{ Body AutomationRuleView }
type automationRunsOutput struct {
	Body struct {
		Items []AutomationRunView `json:"items"`
		Next  int64               `json:"next"`
	}
}

type automationRuleInput struct {
	Body struct {
		Name            string          `json:"name" minLength:"1" maxLength:"120"`
		Enabled         bool            `json:"enabled"`
		Trigger         string          `json:"trigger" enum:"trial.started,user.created,user.granted,user.expiring,user.expired,user.inactive,traffic.90,traffic.100,payment.paid,payment.failed,payment.refunded,trial.not_given,device.bound,device.unbound,balance.topup,discount.ending,node.down,node.up"`
		Conditions      json.RawMessage `json:"conditions,omitempty"`
		Actions         json.RawMessage `json:"actions"`
		CooldownSeconds int64           `json:"cooldown_seconds" minimum:"0" maximum:"31536000"`
		DryRun          bool            `json:"dry_run"`
	}
}

type automationRuleUpdateInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Name            string          `json:"name" minLength:"1" maxLength:"120"`
		Enabled         bool            `json:"enabled"`
		Trigger         string          `json:"trigger" enum:"trial.started,user.created,user.granted,user.expiring,user.expired,user.inactive,traffic.90,traffic.100,payment.paid,payment.failed,payment.refunded,trial.not_given,device.bound,device.unbound,balance.topup,discount.ending,node.down,node.up"`
		Conditions      json.RawMessage `json:"conditions,omitempty"`
		Actions         json.RawMessage `json:"actions"`
		CooldownSeconds int64           `json:"cooldown_seconds" minimum:"0" maximum:"31536000"`
		DryRun          bool            `json:"dry_run"`
	}
}

type automationRunsInput struct {
	Before int64 `query:"before" minimum:"0" default:"0"`
	Limit  int   `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

func (h *handlers) registerAutomation() {
	tags := []string{"automation"}
	huma.Register(h.api, huma.Operation{OperationID: "list-automation-rules", Method: http.MethodGet, Path: "/api/v1/automation/rules", Summary: "Сценарии автоматизации", Tags: tags}, h.listAutomationRules)
	huma.Register(h.api, huma.Operation{OperationID: "create-automation-rule", Method: http.MethodPost, Path: "/api/v1/automation/rules", Summary: "Создать сценарий", Tags: tags, DefaultStatus: http.StatusCreated}, h.createAutomationRule)
	huma.Register(h.api, huma.Operation{OperationID: "update-automation-rule", Method: http.MethodPut, Path: "/api/v1/automation/rules/{id}", Summary: "Изменить сценарий", Tags: tags}, h.updateAutomationRule)
	huma.Register(h.api, huma.Operation{OperationID: "delete-automation-rule", Method: http.MethodDelete, Path: "/api/v1/automation/rules/{id}", Summary: "Удалить сценарий", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteAutomationRule)
	huma.Register(h.api, huma.Operation{OperationID: "list-automation-runs", Method: http.MethodGet, Path: "/api/v1/automation/runs", Summary: "Журнал запусков сценариев", Tags: tags}, h.listAutomationRuns)
}

func automationRuleView(r db.AutomationRule) AutomationRuleView {
	v := AutomationRuleView{ID: r.ID, Name: r.Name, Enabled: r.Enabled != 0, Trigger: r.Trigger,
		Conditions: json.RawMessage(r.Conditions), Actions: json.RawMessage(r.Actions),
		CooldownSeconds: r.CooldownSeconds, DryRun: r.DryRun != 0,
		CreatedAt: store.Time(r.CreatedAt), UpdatedAt: store.Time(r.UpdatedAt), RunsCount: r.RunsCount}
	if r.LastRunAt.Valid {
		t := store.Time(r.LastRunAt.Int64)
		v.LastRunAt = &t
	}
	return v
}

func automationRunView(r db.AutomationRun) AutomationRunView {
	v := AutomationRunView{ID: r.ID, RuleID: r.RuleID, EventID: r.EventID, EventType: r.EventType,
		Status: r.Status, DryRun: r.DryRun != 0, Detail: r.Detail, CreatedAt: store.Time(r.CreatedAt)}
	if r.UserID.Valid {
		v.UserID = &r.UserID.Int64
	}
	if r.TgID.Valid {
		v.TgID = &r.TgID.Int64
	}
	return v
}

func (h *handlers) listAutomationRules(ctx context.Context, _ *struct{}) (*automationRulesOutput, error) {
	rows, err := h.d.Store.Q.ListAutomationRules(ctx)
	if err != nil {
		return nil, err
	}
	out := &automationRulesOutput{Body: make([]AutomationRuleView, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, automationRuleView(r))
	}
	return out, nil
}

func (h *handlers) createAutomationRule(ctx context.Context, in *automationRuleInput) (*automationRuleOutput, error) {
	conds, acts, err := automationRuleBody(in.Body.Name, in.Body.Trigger, in.Body.Conditions, in.Body.Actions)
	if err != nil {
		return nil, err
	}
	now := h.d.Now().Unix()
	r, err := h.d.Store.Q.CreateAutomationRule(ctx, db.CreateAutomationRuleParams{
		Name: strings.TrimSpace(in.Body.Name), Enabled: boolInt(in.Body.Enabled), Trigger: in.Body.Trigger,
		Conditions: conds, Actions: acts, CooldownSeconds: in.Body.CooldownSeconds, DryRun: boolInt(in.Body.DryRun), CreatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "automation.create", "automation", itoa(r.ID), map[string]any{"name": r.Name, "trigger": r.Trigger})
	return &automationRuleOutput{Body: automationRuleView(r)}, nil
}

func (h *handlers) updateAutomationRule(ctx context.Context, in *automationRuleUpdateInput) (*automationRuleOutput, error) {
	conds, acts, err := automationRuleBody(in.Body.Name, in.Body.Trigger, in.Body.Conditions, in.Body.Actions)
	if err != nil {
		return nil, err
	}
	r, err := h.d.Store.Q.UpdateAutomationRule(ctx, db.UpdateAutomationRuleParams{
		ID: in.ID, Name: strings.TrimSpace(in.Body.Name), Enabled: boolInt(in.Body.Enabled), Trigger: in.Body.Trigger,
		Conditions: conds, Actions: acts, CooldownSeconds: in.Body.CooldownSeconds, DryRun: boolInt(in.Body.DryRun), UpdatedAt: h.d.Now().Unix(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "automation.update", "automation", itoa(r.ID), map[string]any{"name": r.Name, "trigger": r.Trigger})
	return &automationRuleOutput{Body: automationRuleView(r)}, nil
}

func (h *handlers) deleteAutomationRule(ctx context.Context, in *automationRuleIDInput) (*struct{}, error) {
	n, err := h.d.Store.Q.DeleteAutomationRule(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "automation.delete", "automation", itoa(in.ID), nil)
	return nil, nil
}

type automationRuleIDInput struct {
	ID int64 `path:"id"`
}

func (h *handlers) listAutomationRuns(ctx context.Context, in *automationRunsInput) (*automationRunsOutput, error) {
	before := in.Before
	if before == 0 {
		before = 1 << 62
	}
	rows, err := h.d.Store.Q.ListAutomationRuns(ctx, db.ListAutomationRunsParams{BeforeID: before, Lim: int32(in.Limit)})
	if err != nil {
		return nil, err
	}
	out := &automationRunsOutput{}
	out.Body.Items = make([]AutomationRunView, 0, len(rows))
	for _, r := range rows {
		out.Body.Items = append(out.Body.Items, automationRunView(r))
	}
	if len(rows) == in.Limit {
		out.Body.Next = rows[len(rows)-1].ID
	}
	return out, nil
}

func automationRuleBody(name, trigger string, rawCond, rawActs json.RawMessage) (string, string, error) {
	if strings.TrimSpace(name) == "" {
		return "", "", fieldErr("body.name", "bad_name")
	}
	if !slices.Contains(automation.Triggers, trigger) {
		return "", "", fieldErr("body.trigger", "bad_trigger")
	}
	cond := "{}"
	if len(rawCond) > 0 && string(rawCond) != "null" {
		var c automation.Conditions
		if err := json.Unmarshal(rawCond, &c); err != nil {
			return "", "", fieldErr("body.conditions", "bad_conditions")
		}
		raw, _ := json.Marshal(c)
		cond = string(raw)
	}
	if len(rawActs) == 0 || string(rawActs) == "null" {
		return "", "", fieldErr("body.actions", "no_actions")
	}
	var acts []automation.Action
	if err := json.Unmarshal(rawActs, &acts); err != nil || len(acts) == 0 {
		return "", "", fieldErr("body.actions", "bad_actions")
	}
	for _, a := range acts {
		if !slices.Contains(automation.ActionTypes, a.Type) {
			return "", "", fieldErr("body.actions", "bad_action")
		}
		if a.Type == automation.ActionExtend && (a.Days < 1 || a.Days > 3650) {
			return "", "", fieldErr("body.actions", "bad_days")
		}
		if a.Type == automation.ActionGrant && a.Bytes <= 0 {
			return "", "", fieldErr("body.actions", "bad_bytes")
		}
		if a.Type == automation.ActionPromo && strings.TrimSpace(a.Code) == "" {
			return "", "", fieldErr("body.actions", "no_code")
		}
		if a.Type == automation.ActionDiscount {
			if a.Percent < 1 || a.Percent > 100 {
				return "", "", fieldErr("body.actions", "bad_percent")
			}
			if a.DiscountDays < 1 || a.DiscountDays > 3650 {
				return "", "", fieldErr("body.actions", "bad_days")
			}
		}
	}
	raw, _ := json.Marshal(acts)
	return cond, string(raw), nil
}

func boolInt(v bool) int32 {
	if v {
		return 1
	}
	return 0
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
