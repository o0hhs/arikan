package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// Where the subscriptions go, while the admin has it on: the nodes sum the bytes by site,
// the panel keeps them by day for a few days. No times, no client addresses.

type TrafficDetailView struct {
	Enabled bool `json:"enabled" doc:"Ноды считают трафик подписок по сайтам"`
	Days    int  `json:"days" doc:"Сколько дней хранить"`
}

type trafficDetailOutput struct{ Body TrafficDetailView }

type putTrafficDetailInput struct {
	Body struct {
		Enabled bool `json:"enabled"`
		Days    int  `json:"days" minimum:"1" maximum:"90"`
	}
}

type SiteBytes struct {
	Site string `json:"site" doc:"Домен второго уровня или адрес; * — остальные сайты"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

type userSitesInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Days int   `query:"days" minimum:"1" maximum:"90" default:"7"`
}

type userSitesOutput struct {
	Body struct {
		Enabled  bool        `json:"enabled"`
		KeepDays int         `json:"keep_days"`
		Items    []SiteBytes `json:"items"`
	}
}

type clearSitesBody struct {
	Rows int64 `json:"rows" doc:"Сколько записей удалено"`
}

type clearSitesOutput struct{ Body clearSitesBody }

func (h *handlers) registerSites() {
	huma.Register(h.api, huma.Operation{OperationID: "get-traffic-detail", Method: http.MethodGet, Path: "/api/v1/traffic-detail", Summary: "Детализация трафика по сайтам", Tags: []string{"filters"}}, h.getTrafficDetail)
	huma.Register(h.api, huma.Operation{OperationID: "put-traffic-detail", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPut, Path: "/api/v1/traffic-detail", Summary: "Включить или выключить детализацию по сайтам",
		Description: "Ноды получают её со своим состоянием; ноды старше неё не считают.", Tags: []string{"filters"}}, h.putTrafficDetail)
	huma.Register(h.api, huma.Operation{OperationID: "clear-user-sites", Method: http.MethodDelete, Path: "/api/v1/traffic-detail/sites", Summary: "Удалить всё собранное о том, куда ходят клиенты",
		Description: "Собранные нодами суммы по сайтам удаляются целиком, за все дни и подписки. Ноды продолжают считать, если детализация включена.",
		Tags:        []string{"filters"}, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.clearUserSites)
	huma.Register(h.api, huma.Operation{OperationID: "user-sites", Method: http.MethodGet, Path: "/api/v1/users/{id}/sites", Summary: "Куда ходит подписка: трафик по сайтам", Tags: []string{"users"}}, h.userSites)
}

func (h *handlers) trafficDetail(ctx context.Context) (TrafficDetailView, error) {
	on, err := h.d.Settings.On(ctx, settings.TrafficDetail)
	return TrafficDetailView{Enabled: on, Days: domain.DetailDays(ctx, h.d.Settings)}, err
}

func (h *handlers) getTrafficDetail(ctx context.Context, _ *struct{}) (*trafficDetailOutput, error) {
	v, err := h.trafficDetail(ctx)
	if err != nil {
		return nil, err
	}
	return &trafficDetailOutput{Body: v}, nil
}

func (h *handlers) putTrafficDetail(ctx context.Context, in *putTrafficDetailInput) (*trafficDetailOutput, error) {
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		if err := settings.Set(ctx, set, settings.KeyTrafficDetail, in.Body.Enabled); err != nil {
			return err
		}
		return settings.Set(ctx, set, settings.KeyTrafficDetailDays, in.Body.Days)
	})
	if err != nil {
		return nil, err
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "traffic_detail.update", "settings", settings.KeyTrafficDetail, map[string]any{"enabled": in.Body.Enabled, "days": in.Body.Days})
	return h.getTrafficDetail(ctx, nil)
}

// clearUserSites drops everything the nodes summed so far: the admin's answer to a user who
// asks what is kept about them, and the way to start over after a week of noise. The nodes
// keep counting when the detail is on; what was collected is gone.
func (h *handlers) clearUserSites(ctx context.Context, _ *struct{}) (*clearSitesOutput, error) {
	var rows int64
	if err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		n, err := q.ClearUserSites(ctx)
		rows = n
		return err
	}); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "traffic_detail.clear", "settings", settings.KeyTrafficDetail, map[string]any{"rows": rows})
	return &clearSitesOutput{Body: clearSitesBody{Rows: rows}}, nil
}

func (h *handlers) userSites(ctx context.Context, in *userSitesInput) (*userSitesOutput, error) {
	if _, err := h.d.Store.Q.GetUser(ctx, in.ID); errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	} else if err != nil {
		return nil, err
	}
	v, err := h.trafficDetail(ctx)
	if err != nil {
		return nil, err
	}
	since := domain.SiteDay(h.d.Now().AddDate(0, 0, -(in.Days - 1)))
	rows, err := h.d.Store.Q.UserSites(ctx, db.UserSitesParams{UserID: in.ID, Since: since, Lim: 50})
	if err != nil {
		return nil, err
	}
	out := &userSitesOutput{}
	out.Body.Enabled, out.Body.KeepDays, out.Body.Items = v.Enabled, v.Days, make([]SiteBytes, 0, len(rows))
	for _, r := range rows {
		out.Body.Items = append(out.Body.Items, SiteBytes{Site: r.Site, Up: r.Up, Down: r.Down})
	}
	return out, nil
}
