package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/store/db"
)

// The business side of the panel: what the subscriptions earn, who pays and who leaves.
// The figures are computed from the payments and users the panel already keeps; nothing
// here changes a payment, a subscription or a tariff.

type analyticsInput struct {
	Range string `query:"range" enum:"30d,90d,365d" default:"30d" doc:"Период: 30 дней, 90 дней или год"`
}

type AnalyticsSummary struct {
	RevenueRub       int64   `json:"revenue_rub" doc:"Оплачено в рублях за период, копейки"`
	RevenueStars     int64   `json:"revenue_stars" doc:"Оплачено в Stars за период; в рублёвую выручку не входит"`
	Payments         int64   `json:"payments" doc:"Применённых оплат за период"`
	PayingUsers      int64   `json:"paying_users" doc:"Разных платящих за период"`
	ActiveUsers      int64   `json:"active_users" doc:"Активных подписок сейчас"`
	NewUsers         int64   `json:"new_users" doc:"Новых клиентов за период"`
	MRR              int64   `json:"mrr_rub" doc:"Повторяющаяся выручка в месяц, копейки"`
	ARR              int64   `json:"arr_rub" doc:"MRR × 12, копейки"`
	ARPU             int64   `json:"arpu_rub" doc:"MRR на активную подписку, копейки"`
	ARPPU            int64   `json:"arppu_rub" doc:"MRR на платящего клиента, копейки"`
	LTV              int64   `json:"ltv_rub" doc:"Средняя выручка на платящего клиента за всё время, копейки"`
	TotalRevenueRub  int64   `json:"total_revenue_rub" doc:"Вся рублёвая выручка, копейки"`
	TotalPayingUsers int64   `json:"total_paying_users" doc:"Все разные платящие клиенты"`
	TrialStarted     int64   `json:"trial_started" doc:"Пробных периодов начато за период"`
	TrialConverted   int64   `json:"trial_converted" doc:"Из них дошли до оплаты"`
	TrialConversion  float64 `json:"trial_conversion" doc:"Конверсия триала в оплату, проценты"`
	Expired          int64   `json:"expired" doc:"Подписок истекло за период"`
	Churned          int64   `json:"churned" doc:"Из них не продлили"`
	Churn            float64 `json:"churn" doc:"Отток истёкших подписок, проценты"`
}

type AnalyticsRevenuePoint struct {
	Day      time.Time `json:"day"`
	Rub      int64     `json:"rub"`
	Stars    int64     `json:"stars"`
	Payments int64     `json:"payments"`
}

type AnalyticsTariff struct {
	ID       *int64  `json:"id,omitempty" doc:"null — тариф удалён, осталось имя, под которым продавали"`
	Name     string  `json:"name"`
	Payments int64   `json:"payments"`
	Rub      int64   `json:"rub"`
	Stars    int64   `json:"stars"`
	Share    float64 `json:"share" doc:"Доля в рублёвой выручке периода, проценты"`
}

type AnalyticsProvider struct {
	Provider string  `json:"provider"`
	Payments int64   `json:"payments"`
	Rub      int64   `json:"rub"`
	Stars    int64   `json:"stars"`
	Share    float64 `json:"share" doc:"Доля в рублёвой выручке периода, проценты"`
}

type AnalyticsCohort struct {
	Month  string    `json:"month" doc:"YYYY-MM"`
	Users  int64     `json:"users"`
	Paying []int64   `json:"paying" doc:"Платили в месяц 0..5 после регистрации"`
	Rates  []float64 `json:"rates" doc:"То же в процентах от когорты"`
}

type AnalyticsCampaign struct {
	ID      int64  `json:"id"`
	Tag     string `json:"tag"`
	Name    string `json:"name"`
	Bought  int64  `json:"bought"`
	Renewed int64  `json:"renewed"`
	Rub     int64  `json:"rub"`
}

type analyticsOutput struct {
	Body struct {
		GeneratedAt time.Time               `json:"generated_at"`
		Range       string                  `json:"range"`
		Summary     AnalyticsSummary        `json:"summary"`
		Revenue     []AnalyticsRevenuePoint `json:"revenue"`
		Tariffs     []AnalyticsTariff       `json:"tariffs"`
		Providers   []AnalyticsProvider     `json:"providers"`
		Cohorts     []AnalyticsCohort       `json:"cohorts"`
		Campaigns   []AnalyticsCampaign     `json:"campaigns"`
	}
}

func (h *handlers) registerAnalytics() {
	huma.Register(h.api, huma.Operation{
		OperationID: "analytics",
		Method:      http.MethodGet,
		Path:        "/api/v1/analytics",
		Summary:     "Аналитика: выручка, MRR, конверсия и удержание",
		Description: "Считается по применённым оплатам и подпискам. Stars показаны отдельно и не входят в рублёвую выручку.",
		Tags:        []string{"stats"},
	}, h.analytics)
}

func (h *handlers) analytics(ctx context.Context, in *analyticsInput) (*analyticsOutput, error) {
	now := h.d.Now().UTC()
	since := analyticsSince(now, in.Range)
	cohortSince := analyticsCohortSince(now)
	q := h.d.Store.Q

	sum, err := q.AnalyticsSummary(ctx, db.AnalyticsSummaryParams{Now: now.Unix(), Since: since.Unix()})
	if err != nil {
		return nil, err
	}
	trials, err := q.AnalyticsTrials(ctx, since.Unix())
	if err != nil {
		return nil, err
	}
	churn, err := q.AnalyticsChurn(ctx, db.AnalyticsChurnParams{Since: since.Unix(), Now: now.Unix()})
	if err != nil {
		return nil, err
	}
	daily, err := q.AnalyticsRevenueDaily(ctx, since.Unix())
	if err != nil {
		return nil, err
	}
	byTariff, err := q.AnalyticsByTariff(ctx, since.Unix())
	if err != nil {
		return nil, err
	}
	byProvider, err := q.AnalyticsByProvider(ctx, since.Unix())
	if err != nil {
		return nil, err
	}
	cohortRows, err := q.AnalyticsCohorts(ctx, cohortSince.Unix())
	if err != nil {
		return nil, err
	}
	campaigns, err := q.AnalyticsCampaigns(ctx, since.Unix())
	if err != nil {
		return nil, err
	}

	out := &analyticsOutput{}
	out.Body.GeneratedAt = now
	out.Body.Range = in.Range
	out.Body.Summary = analyticsSummaryOf(sum, trials, churn)
	out.Body.Revenue = analyticsRevenue(daily, now, in.Range)
	out.Body.Tariffs = analyticsTariffs(byTariff, out.Body.Summary.RevenueRub)
	out.Body.Providers = analyticsProviders(byProvider, out.Body.Summary.RevenueRub)
	out.Body.Cohorts = analyticsCohorts(cohortRows)
	out.Body.Campaigns = analyticsCampaigns(campaigns)
	return out, nil
}

func analyticsSince(now time.Time, rng string) time.Time {
	switch rng {
	case "90d":
		return now.Add(-90 * 24 * time.Hour)
	case "365d":
		return now.Add(-365 * 24 * time.Hour)
	default:
		return now.Add(-30 * 24 * time.Hour)
	}
}

// analyticsCohortSince is the first day of the month five months back: six monthly cohorts
// including the one that is running now.
func analyticsCohortSince(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month()-5, 1, 0, 0, 0, 0, time.UTC)
}

func analyticsSummaryOf(s db.AnalyticsSummaryRow, t db.AnalyticsTrialsRow, c db.AnalyticsChurnRow) AnalyticsSummary {
	out := AnalyticsSummary{
		RevenueRub: s.RevenueRub, RevenueStars: s.RevenueStars, Payments: s.Payments,
		PayingUsers: s.PayingUsers, ActiveUsers: s.ActiveUsers, NewUsers: s.NewUsers,
		MRR: s.MrrRub, ARR: s.MrrRub * 12,
		TotalRevenueRub: s.TotalRevenueRub, TotalPayingUsers: s.TotalPayingUsers,
		TrialStarted: t.Started, TrialConverted: t.Converted,
		Expired: c.Expired, Churned: c.Churned,
	}
	if s.ActiveUsers > 0 {
		out.ARPU = s.MrrRub / s.ActiveUsers
	}
	if s.PayingUsers > 0 {
		out.ARPPU = s.MrrRub / s.PayingUsers
	}
	if s.TotalPayingUsers > 0 {
		out.LTV = s.TotalRevenueRub / s.TotalPayingUsers
	}
	if t.Started > 0 {
		out.TrialConversion = float64(t.Converted) / float64(t.Started) * 100
	}
	if c.Expired > 0 {
		out.Churn = float64(c.Churned) / float64(c.Expired) * 100
	}
	return out
}

// analyticsRevenue fills the quiet days with zero, so the chart has one point per day and
// no gap is drawn as a straight line between two distant payments.
func analyticsRevenue(rows []db.AnalyticsRevenueDailyRow, now time.Time, rng string) []AnalyticsRevenuePoint {
	byDay := make(map[string]db.AnalyticsRevenueDailyRow, len(rows))
	for _, r := range rows {
		byDay[r.Day.UTC().Format("2006-01-02")] = r
	}
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	days := 30
	if rng == "90d" {
		days = 90
	} else if rng == "365d" {
		days = 365
	}
	out := make([]AnalyticsRevenuePoint, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := end.AddDate(0, 0, -i)
		r, ok := byDay[day.Format("2006-01-02")]
		if !ok {
			r.Day = day
		}
		out = append(out, AnalyticsRevenuePoint{Day: day, Rub: r.Rub, Stars: r.Stars, Payments: r.Payments})
	}
	return out
}

func analyticsTariffs(rows []db.AnalyticsByTariffRow, revenue int64) []AnalyticsTariff {
	out := make([]AnalyticsTariff, 0, len(rows))
	for _, r := range rows {
		v := AnalyticsTariff{Name: r.TariffName, Payments: r.Payments, Rub: r.Rub, Stars: r.Stars}
		if r.TariffID.Valid {
			id := r.TariffID.Int64
			v.ID = &id
		}
		if revenue > 0 {
			v.Share = float64(r.Rub) / float64(revenue) * 100
		}
		out = append(out, v)
	}
	return out
}

func analyticsProviders(rows []db.AnalyticsByProviderRow, revenue int64) []AnalyticsProvider {
	out := make([]AnalyticsProvider, 0, len(rows))
	for _, r := range rows {
		v := AnalyticsProvider{Provider: r.Provider, Payments: r.Payments, Rub: r.Rub, Stars: r.Stars}
		if revenue > 0 {
			v.Share = float64(r.Rub) / float64(revenue) * 100
		}
		out = append(out, v)
	}
	return out
}

func analyticsCohorts(rows []db.AnalyticsCohortsRow) []AnalyticsCohort {
	byMonth := make(map[string]*AnalyticsCohort, 6)
	order := make([]string, 0, 6)
	for _, r := range rows {
		month := r.Cohort.UTC().Format("2006-01")
		c := byMonth[month]
		if c == nil {
			c = &AnalyticsCohort{Month: month, Users: r.Users, Paying: make([]int64, 6), Rates: make([]float64, 6)}
			byMonth[month] = c
			order = append(order, month)
		}
		offset := int(r.MonthOffset)
		if offset < 0 || offset > 5 {
			continue
		}
		c.Paying[offset] = r.Paying
	}
	sort.Strings(order)
	out := make([]AnalyticsCohort, 0, len(order))
	for _, month := range order {
		c := byMonth[month]
		for i, n := range c.Paying {
			if c.Users > 0 {
				c.Rates[i] = float64(n) / float64(c.Users) * 100
			}
		}
		out = append(out, *c)
	}
	return out
}

func analyticsCampaigns(rows []db.AnalyticsCampaignsRow) []AnalyticsCampaign {
	out := make([]AnalyticsCampaign, 0, len(rows))
	for _, r := range rows {
		out = append(out, AnalyticsCampaign{ID: r.ID, Tag: r.Tag, Name: r.Name, Bought: r.Bought, Renewed: r.Renewed, Rub: r.Rub})
	}
	return out
}
