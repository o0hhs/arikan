package billing

import (
	"fmt"
	"strings"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/store/db"
)

// Describe is tariff t bought for a term of days in a line, "30 days · 100 GB · 3
// devices", in lang ("en", else Russian): invoices, the bot and the Mini App show it.
func Describe(t db.Tariff, days int64, lang string) string {
	return TermLabel(t, days, lang) + " · " + DescribeLimits(t, lang)
}

// TermLabel is a term of tariff t in a few words: "30 days", "3 months" for a tariff that
// ends on a billing day, "no end date".
func TermLabel(t db.Tariff, days int64, lang string) string {
	pick := func(ru, en string) string {
		if lang == "en" {
			return en
		}
		return ru
	}
	switch {
	case days <= 0:
		return pick("бессрочно", "no end date")
	case t.BillingDay.Valid:
		n := model.TermMonths(days)
		if n == 1 {
			return pick("1 мес.", "1 month")
		}
		return fmt.Sprintf(pick("%d мес.", "%d months"), n)
	}
	return fmt.Sprintf(pick("%d дн.", "%d days"), days)
}

// DescribeOffer is a tariff on sale in a line: Describe with its one term, or the range
// of its terms, "7 days – 90 days · 100 GB · 3 devices".
func DescribeOffer(o Offer, lang string) string {
	switch len(o.Terms) {
	case 0:
		return Describe(o.Tariff, o.Tariff.DurationDays, lang)
	case 1:
		return Describe(o.Tariff, o.Terms[0].Days, lang)
	}
	var lo, hi int64 = -1, -1
	forever := false
	for _, t := range o.Terms {
		if t.Days <= 0 {
			forever = true
			continue
		}
		if lo < 0 || t.Days < lo {
			lo = t.Days
		}
		hi = max(hi, t.Days)
	}
	if forever {
		hi = 0
	}
	return TermLabel(o.Tariff, lo, lang) + " – " + TermLabel(o.Tariff, hi, lang) + " · " + DescribeLimits(o.Tariff, lang)
}

// DescribeLimits is what tariff t gives whatever the term: "100 GB · 3 devices".
func DescribeLimits(t db.Tariff, lang string) string {
	en := lang == "en"
	pick := func(ru, en_ string) string {
		if en {
			return en_
		}
		return ru
	}
	parts := []string{}
	if t.TrafficLimit.Valid {
		parts = append(parts, fmt.Sprintf(pick("%d ГБ", "%d GB"), t.TrafficLimit.Int64>>30))
	} else {
		parts = append(parts, pick("трафик без лимита", "unlimited traffic"))
	}
	if t.DeviceLimit.Valid && t.DeviceMode == domain.DeviceModeOnline {
		// The buyer is not told of places: any number of devices, so many online at once.
		parts = append(parts, fmt.Sprintf(pick("устройств одновременно: %d", "devices at once: %d"), t.DeviceLimit.Int64))
	} else if t.DeviceLimit.Valid {
		parts = append(parts, fmt.Sprintf(pick("устройств: %d", "devices: %d"), t.DeviceLimit.Int64))
	}
	if t.SpeedLimit.Valid {
		parts = append(parts, fmt.Sprintf(pick("до %d Мбит/с", "up to %d Mbit/s"), t.SpeedLimit.Int64))
	}
	return strings.Join(parts, " · ")
}
