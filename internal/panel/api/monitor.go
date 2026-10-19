package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/monitor"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The public monitoring page as the admin sets it up: its own address, its password and
// what it shows. The password is never given back, only whether one is set.

// monitorFieldErr names the field the document refused and what it refused it for.
func monitorFieldErr(err error, value any) error {
	var bad monitor.BadBlock
	if errors.As(err, &bad) {
		return fieldErrs(fieldDetail("body.blocks", err.Error(), bad.Block))
	}
	return fieldErrs(fieldDetail("body.path", err.Error(), value))
}

type MonitorView struct {
	Enabled bool `json:"enabled" doc:"Показывать страницу по публичной ссылке"`
	// Path is the page's own address segment; "" while the page was never turned on.
	Path string `json:"path" doc:"Адрес страницы в ссылке"`
	// HasPassword: a password is set, so turning the page on is enough to open it.
	HasPassword bool     `json:"has_password"`
	Blocks      []string `json:"blocks" doc:"Что показывает страница"`
	// Available: the blocks this panel has, in the order the page lays them out.
	Available []string `json:"available"`
	// MinPassword: the shortest password the panel takes (auth.MinPassword).
	MinPassword int `json:"min_password"`
}

type monitorOutput struct{ Body MonitorView }

type putMonitorInput struct {
	Body struct {
		Enabled bool     `json:"enabled"`
		Blocks  []string `json:"blocks" maxItems:"16"`
	}
}

type setMonitorPasswordInput struct {
	Body struct {
		// A struct tag takes no constant: minLength repeats auth.MinPassword
		// (TestMonitorPasswordTagIsMinPassword).
		Password string `json:"password" minLength:"12" maxLength:"1024" doc:"Пароль страницы; старый и все его сессии перестают работать"`
	}
}

type rotateMonitorInput struct {
	Body struct {
		Path string `json:"path" minLength:"8" maxLength:"64" pattern:"^[A-Za-z0-9_-]+$" doc:"Новый адрес страницы; старый перестаёт работать"`
	}
}

func (h *handlers) registerMonitor() {
	tags := []string{"settings"}
	huma.Register(h.api, huma.Operation{OperationID: "get-monitor", Method: http.MethodGet, Path: "/api/v1/monitor", Summary: "Публичная страница мониторинга: включена, её ссылка и блоки", Tags: tags}, h.getMonitor)
	huma.Register(h.api, huma.Operation{OperationID: "put-monitor", Method: http.MethodPut, Path: "/api/v1/monitor", Summary: "Включить или выключить страницу мониторинга и выбрать её блоки",
		Description: "Пока пароль не задан, страница не открывается, даже если она включена.", Tags: tags}, h.putMonitor)
	huma.Register(h.api, huma.Operation{OperationID: "set-monitor-password", Method: http.MethodPost, Path: "/api/v1/monitor/password", Summary: "Задать пароль страницы мониторинга",
		Description: "Пароль не возвращается: панель хранит только его хеш. Старый пароль и все открытые сессии страницы перестают работать.",
		Tags:        tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.setMonitorPassword)
	huma.Register(h.api, huma.Operation{OperationID: "clear-monitor-password", Method: http.MethodDelete, Path: "/api/v1/monitor/password", Summary: "Убрать пароль: страница закрывается",
		Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.clearMonitorPassword)
	huma.Register(h.api, huma.Operation{OperationID: "rotate-monitor-path", Method: http.MethodPost, Path: "/api/v1/monitor/path", Summary: "Сменить адрес страницы мониторинга",
		Description: "Старая ссылка перестаёт работать; пароль остаётся.", Tags: tags}, h.rotateMonitorPath)
}

func (h *handlers) monitorView(ctx context.Context) (MonitorView, error) {
	c, err := monitor.Load(ctx, h.d.Settings)
	if err != nil {
		return MonitorView{}, err
	}
	return MonitorView{
		Enabled: c.Enabled, Path: c.Path, HasPassword: c.Hash != "",
		Blocks: c.Blocks, Available: slices.Clone(monitor.Blocks), MinPassword: auth.MinPassword,
	}, nil
}

func (h *handlers) getMonitor(ctx context.Context, _ *struct{}) (*monitorOutput, error) {
	v, err := h.monitorView(ctx)
	if err != nil {
		return nil, err
	}
	return &monitorOutput{Body: v}, nil
}

func (h *handlers) putMonitor(ctx context.Context, in *putMonitorInput) (*monitorOutput, error) {
	// The document knows its own rules: the request is one (a link is made below, so an
	// empty path here is not a wrong one).
	if err := (monitor.Config{Enabled: in.Body.Enabled, Blocks: in.Body.Blocks}).Validate(); err != nil {
		return nil, monitorFieldErr(err, in.Body.Blocks)
	}
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		c, err := monitor.Load(ctx, settings.New(q))
		if err != nil {
			return err
		}
		c.Enabled = in.Body.Enabled
		// The blocks as the page's own order, with nothing this panel does not have.
		var blocks []string
		for _, b := range monitor.Blocks {
			if slices.Contains(in.Body.Blocks, b) {
				blocks = append(blocks, b)
			}
		}
		c.Blocks = blocks
		if c.Path == "" {
			c.Path = secure.Token(monitor.PathLen)
		}
		return monitor.Save(ctx, settings.New(q), c)
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "monitor.update", "settings", settings.KeyMonitor, map[string]any{"enabled": in.Body.Enabled, "blocks": in.Body.Blocks})
	return h.getMonitor(ctx, nil)
}

func (h *handlers) setMonitorPassword(ctx context.Context, in *setMonitorPasswordInput) (*monitorOutput, error) {
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		c, err := monitor.Load(ctx, settings.New(q))
		if err != nil {
			return err
		}
		if err := monitor.SetPassword(&c, in.Body.Password); err != nil {
			return err
		}
		if c.Path == "" {
			c.Path = secure.Token(monitor.PathLen)
		}
		return monitor.Save(ctx, settings.New(q), c)
	})
	if err != nil {
		if err == monitor.ErrWeakPassword {
			return nil, fieldErrs(fieldDetail("body.password", "monitor_password_short", auth.MinPassword))
		}
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "monitor.password", "settings", settings.KeyMonitor, nil)
	return h.getMonitor(ctx, nil)
}

// clearMonitorPassword: no password, no page. What the nodes summed is untouched.
func (h *handlers) clearMonitorPassword(ctx context.Context, _ *struct{}) (*monitorOutput, error) {
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		c, err := monitor.Load(ctx, settings.New(q))
		if err != nil {
			return err
		}
		c.Hash = ""
		return monitor.Save(ctx, settings.New(q), c)
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "monitor.password.clear", "settings", settings.KeyMonitor, nil)
	return h.getMonitor(ctx, nil)
}

func (h *handlers) rotateMonitorPath(ctx context.Context, in *rotateMonitorInput) (*monitorOutput, error) {
	if err := (monitor.Config{Path: in.Body.Path}).Validate(); err != nil {
		return nil, monitorFieldErr(err, in.Body.Path)
	}
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		c, err := monitor.Load(ctx, settings.New(q))
		if err != nil {
			return err
		}
		c.Path = in.Body.Path
		return monitor.Save(ctx, settings.New(q), c)
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "monitor.path", "settings", settings.KeyMonitor, nil)
	return h.getMonitor(ctx, nil)
}
