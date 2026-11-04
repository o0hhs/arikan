package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// DevicePackView is a pack of extra device places as the admin's page shows it.
type DevicePackView struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	Slots      int64     `json:"slots" doc:"Сколько мест пакет добавляет к лимиту устройств тарифа"`
	PriceRub   int64     `json:"price_rub" doc:"Цена за месяц, в копейках"`
	PriceStars *int64    `json:"price_stars,omitempty" doc:"Цена за месяц в Stars; без значения — только рубли"`
	Sort       int64     `json:"sort"`
	OnSale     bool      `json:"on_sale"`
	Archived   bool      `json:"archived"`
	CreatedAt  time.Time `json:"created_at"`
}

// DevicePackBody is a pack as the admin saves it.
type DevicePackBody struct {
	Title      string `json:"title" minLength:"1" maxLength:"120"`
	Slots      int64  `json:"slots" minimum:"1" maximum:"47" doc:"Сколько мест пакет добавляет (панель держит до 50 устройств)"`
	PriceRub   int64  `json:"price_rub" minimum:"0" doc:"Цена за месяц, в копейках"`
	PriceStars *int64 `json:"price_stars,omitempty" minimum:"0"`
	Sort       int64  `json:"sort" minimum:"0"`
	OnSale     bool   `json:"on_sale"`
}

type devicePackListOutput struct {
	Body []DevicePackView `json:"items"`
}

type devicePackOneOutput struct{ Body DevicePackView }

func (h *handlers) registerDevicePacks() {
	tags := []string{"device-packs"}
	huma.Register(h.api, huma.Operation{OperationID: "list-device-packs", Method: http.MethodGet, Path: "/api/v1/device-packs", Summary: "Пакеты устройств", Tags: tags}, h.listDevicePacks)
	huma.Register(h.api, huma.Operation{OperationID: "create-device-pack", Method: http.MethodPost, Path: "/api/v1/device-packs", Summary: "Создать пакет устройств", Tags: tags}, h.createDevicePack)
	huma.Register(h.api, huma.Operation{OperationID: "update-device-pack", Method: http.MethodPut, Path: "/api/v1/device-packs/{id}", Summary: "Изменить пакет устройств", Tags: tags}, h.updateDevicePack)
	huma.Register(h.api, huma.Operation{OperationID: "delete-device-pack", Method: http.MethodDelete, Path: "/api/v1/device-packs/{id}", Summary: "Убрать пакет устройств из продажи (в архив)", Tags: tags}, h.archiveDevicePack)
	huma.Register(h.api, huma.Operation{OperationID: "restore-device-pack", Method: http.MethodPost, Path: "/api/v1/device-packs/{id}/restore", Summary: "Вернуть пакет устройств из архива", Tags: tags}, h.restoreDevicePack)
}

func devicePackView(p db.DevicePack) DevicePackView {
	return DevicePackView{
		ID: p.ID, Title: p.Title, Slots: int64(p.Slots), PriceRub: p.PriceRub,
		PriceStars: func() *int64 {
			if p.PriceStars.Valid {
				v := p.PriceStars.Int64
				return &v
			}
			return nil
		}(),
		Sort: p.Sort, OnSale: p.OnSale, Archived: p.Archived, CreatedAt: time.Unix(p.CreatedAt, 0),
	}
}

func validateDevicePackBody(in DevicePackBody) error {
	if strings.TrimSpace(in.Title) == "" {
		return errors.New("bad_title")
	}
	// The panel's own hard ceiling is fifty devices; a pack may not promise past it.
	if in.Slots < 1 || in.Slots > 47 {
		return errors.New("bad_slots")
	}
	if in.PriceRub < 0 {
		return errors.New("bad_price")
	}
	if in.PriceStars != nil && *in.PriceStars < 0 {
		return errors.New("bad_price")
	}
	return nil
}

func (h *handlers) listDevicePacks(ctx context.Context, _ *struct{}) (*devicePackListOutput, error) {
	xs, err := h.d.Store.Q.ListDevicePacks(ctx)
	if err != nil {
		return nil, err
	}
	o := &devicePackListOutput{Body: make([]DevicePackView, 0, len(xs))}
	for _, p := range xs {
		o.Body = append(o.Body, devicePackView(p))
	}
	return o, nil
}

func (h *handlers) createDevicePack(ctx context.Context, in *struct{ Body DevicePackBody }) (*devicePackOneOutput, error) {
	if err := validateDevicePackBody(in.Body); err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	p, err := h.d.Store.Q.CreateDevicePack(ctx, db.CreateDevicePackParams{
		Title: strings.TrimSpace(in.Body.Title), Slots: int32(in.Body.Slots), PriceRub: in.Body.PriceRub,
		PriceStars: domain.NullInt(in.Body.PriceStars), Sort: in.Body.Sort, OnSale: in.Body.OnSale,
		Archived: false, CreatedAt: h.d.Now().Unix(),
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "device_pack.create", "device_pack", strconv64(p.ID), map[string]any{"title": p.Title})
	return &devicePackOneOutput{Body: devicePackView(p)}, nil
}

func (h *handlers) updateDevicePack(ctx context.Context, in *struct {
	ID   int64 `path:"id"`
	Body DevicePackBody
}) (*devicePackOneOutput, error) {
	if err := validateDevicePackBody(in.Body); err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if _, err := h.d.Store.Q.GetDevicePack(ctx, in.ID); errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	} else if err != nil {
		return nil, err
	}
	p, err := h.d.Store.Q.UpdateDevicePack(ctx, db.UpdateDevicePackParams{
		Title: strings.TrimSpace(in.Body.Title), Slots: int32(in.Body.Slots), PriceRub: in.Body.PriceRub,
		PriceStars: domain.NullInt(in.Body.PriceStars), Sort: in.Body.Sort, OnSale: in.Body.OnSale, ID: in.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "device_pack.update", "device_pack", strconv64(in.ID), nil)
	return &devicePackOneOutput{Body: devicePackView(p)}, nil
}

func (h *handlers) archiveDevicePack(ctx context.Context, in *struct {
	ID int64 `path:"id"`
}) (*struct{}, error) {
	n, err := h.d.Store.Q.ArchiveDevicePack(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "device_pack.archive", "device_pack", strconv64(in.ID), nil)
	return &struct{}{}, nil
}

func (h *handlers) restoreDevicePack(ctx context.Context, in *struct {
	ID int64 `path:"id"`
}) (*devicePackOneOutput, error) {
	n, err := h.d.Store.Q.UnarchiveDevicePack(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	p, err := h.d.Store.Q.GetDevicePack(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "device_pack.restore", "device_pack", strconv64(in.ID), nil)
	return &devicePackOneOutput{Body: devicePackView(p)}, nil
}
