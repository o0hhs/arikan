package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
)

// The removal of inactive subscriptions (domain/inactive.go): the admin's rules, and who
// they reach now. Turning the removal on, or making it reach further, is the admin's own
// session: a leaked API key must not delete subscribers.

type lifecycleOutput struct{ Body domain.Lifecycle }

type lifecycleInput struct{ Body domain.Lifecycle }

// InactiveView is a subscription the removal rules reach: removed by the next round of the
// housekeeping, or kept for keep.
type InactiveView struct {
	UserID    int64      `json:"user_id"`
	UserName  string     `json:"user_name"`
	TariffID  *int64     `json:"tariff_id"`
	ExpiresAt time.Time  `json:"expires_at" doc:"Когда кончился срок"`
	OnlineAt  *time.Time `json:"online_at" doc:"Последнее подключение; null — не подключался"`
	Keep      string     `json:"keep" enum:",balance,payment,grant" doc:"Почему подписка останется: balance — деньги на балансе привязанного аккаунта, payment — платёж ещё не применён или пополнение ждёт проверки, grant — действуют пакеты трафика; пусто — удалится"`
}

type inactiveInput struct {
	Days int `query:"days" minimum:"0" maximum:"3650" doc:"Посмотреть, кого достанет правило с этим числом дней; 0 — с сохранённым"`
}

// inactiveShown is how many subscriptions the list of the inactive names one by one.
const inactiveShown = 200

type inactiveOutput struct {
	Body struct {
		On    bool           `json:"on" doc:"Правило включено: следующий проход удалит тех, кого ничто не держит"`
		Days  int            `json:"days" doc:"Число дней, с которым составлен список"`
		Due   int            `json:"due" doc:"Сколько подписок удалится"`
		Kept  int            `json:"kept" doc:"Сколько правило достаёт, но оставит"`
		Items []InactiveView `json:"items" doc:"Первые 200 по сроку, дольше всех кончившиеся первыми"`
	}
}

func (h *handlers) registerLifecycle() {
	tags := []string{"users"}
	huma.Register(h.api, huma.Operation{OperationID: "get-lifecycle", Method: http.MethodGet, Path: "/api/v1/lifecycle",
		Summary: "Правила удаления неактивных подписок", Tags: tags}, h.getLifecycle)
	huma.Register(h.api, huma.Operation{OperationID: "update-lifecycle", Method: http.MethodPut, Path: "/api/v1/lifecycle",
		Summary: "Изменить правила удаления неактивных подписок", Description: "Включение удаления и сокращение срока — только сессия админа: ключу API — 403 session_only.", Tags: tags}, h.updateLifecycle)
	huma.Register(h.api, huma.Operation{OperationID: "list-inactive-users", Method: http.MethodGet, Path: "/api/v1/lifecycle/inactive",
		Summary: "Кого достаёт удаление неактивных: пробный прогон, ничего не удаляет", Tags: tags}, h.listInactive)
}

func (h *handlers) getLifecycle(ctx context.Context, _ *struct{}) (*lifecycleOutput, error) {
	l, err := domain.LoadLifecycle(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	return &lifecycleOutput{Body: l}, nil
}

func (h *handlers) updateLifecycle(ctx context.Context, in *lifecycleInput) (*lifecycleOutput, error) {
	l := in.Body
	if err := l.Validate(); err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := domain.LoadLifecycle(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	// A standing rule that deletes subscribers is the admin's own call: a leaked key may
	// not decide who loses a subscription.
	if l.Removal != cur.Removal {
		if err := requireSession(ctx, "removal"); err != nil {
			return nil, err
		}
	}
	if err := settings.Set(ctx, h.d.Settings, domain.KeyLifecycle, l); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "lifecycle.update", "", "", map[string]any{"removal": l.Removal})
	return &lifecycleOutput{Body: l}, nil
}

func (h *handlers) listInactive(ctx context.Context, in *inactiveInput) (*inactiveOutput, error) {
	rules, err := domain.LoadLifecycle(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	r := rules.Removal
	if in.Days > 0 {
		if in.Days < domain.MinRemovalDays || in.Days > domain.MaxRemovalDays {
			return nil, fieldErr("query.days", "bad_removal_days")
		}
		r.Days = in.Days
	}
	list, err := h.d.Users.Inactive(ctx, r)
	if err != nil {
		return nil, err
	}
	out := &inactiveOutput{}
	b := &out.Body
	b.On, b.Days, b.Items = rules.Removal.On, r.Days, []InactiveView{}
	for _, u := range list {
		if u.Keep == "" {
			b.Due++
		} else {
			b.Kept++
		}
		if len(b.Items) < inactiveShown {
			b.Items = append(b.Items, InactiveView{UserID: u.ID, UserName: u.Name, TariffID: store.Int64Ptr(u.TariffID), ExpiresAt: u.ExpiresAt,
				OnlineAt: store.TimePtr(u.OnlineAt), Keep: u.Keep})
		}
	}
	return out, nil
}
