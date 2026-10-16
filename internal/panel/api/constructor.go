package api

import (
	"context"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/settings"
)

// The tariff constructor in the admin API: the base tariff and the multipliers live with the
// payment settings (PaymentsSettingsView), and are saved with them. The types are here so the
// two sides of that form name the same shape.

// CoefficientsView is the multipliers as the panel reports them.
type CoefficientsView struct {
	Traffic  float64 `json:"traffic" doc:"Множитель трафика: 1 — гигабайт сверх тарифа стоит как у самого тарифа"`
	Devices  float64 `json:"devices" doc:"Множитель устройств"`
	Speed    float64 `json:"speed" doc:"Множитель скорости"`
	Duration float64 `json:"duration" doc:"Множитель срока"`
}

// CoefficientsBody is the multipliers a request changes; a field left out keeps its value.
type CoefficientsBody struct {
	Traffic  *float64 `json:"traffic,omitempty" minimum:"0.1" maximum:"10"`
	Devices  *float64 `json:"devices,omitempty" minimum:"0.1" maximum:"10"`
	Speed    *float64 `json:"speed,omitempty" minimum:"0.1" maximum:"10"`
	Duration *float64 `json:"duration,omitempty" minimum:"0.1" maximum:"10"`
}

// saveCoefficients writes the multipliers of the constructor on the caller's transaction:
// only the ones the request names change, and each is checked to be inside the range the
// panel prices with (a plain zero would price everything at nothing).
func saveCoefficients(ctx context.Context, set *settings.Settings, in *CoefficientsBody) error {
	if in == nil {
		return nil
	}
	if err := checkCoefficients(in); err != nil {
		return err
	}
	c := billing.DefaultCoefficients()
	if got, _, err := settings.GetOver(ctx, set, billing.KeyCoefficients, c); err == nil {
		c = got
	}
	for dst, v := range map[*float64]*float64{&c.Traffic: in.Traffic, &c.Devices: in.Devices, &c.Speed: in.Speed, &c.Duration: in.Duration} {
		if v != nil {
			*dst = *v
		}
	}
	return settings.Set(ctx, set, billing.KeyCoefficients, c)
}

// checkCoefficients refuses a multiplier outside 0.1–10. The huma tags say the same; this is
// what holds when a number arrives another way (and a NaN is refused here, not stored).
func checkCoefficients(in *CoefficientsBody) error {
	for name, v := range map[string]*float64{"traffic": in.Traffic, "devices": in.Devices, "speed": in.Speed, "duration": in.Duration} {
		if v == nil {
			continue
		}
		if *v < 0.1 || *v > 10 || *v != *v {
			return fieldErr("body.coefficients."+name, "bad_coefficient")
		}
	}
	return nil
}
