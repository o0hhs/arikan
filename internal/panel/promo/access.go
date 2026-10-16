package promo

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/store/db"
)

// An access code opens one tariff the shop keeps hidden (tariffs.on_sale = 0). Redeeming it
// gives the Telegram account an access (promo_access): with it, the shop offers the tariff and
// the checkout takes the order, while for everyone else both refuse as if the tariff were
// gone. Two modes, the code's type:
//
//	access_paid: the tariff is bought at its own price;
//	access_free: a subscription on the tariff is given right away (like the free plan; it is
//	             the caller that makes the subscription, this package only keeps the access).
//
// The tariff of a code is the single id in promo_codes.tariff_ids, which the panel's
// validation and the database's check keep it at.

// accessTariff is the tariff a code opens: the single id in its list.
func accessTariff(p db.PromoCode) (int64, bool) {
	s := strings.TrimSpace(p.TariffIds)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, ",") {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// TariffOf is the tariff an access code opens; ok is false for any other code. The Mini App
// uses it to open the shop at the right card after a code was taken.
func TariffOf(p db.PromoCode) (int64, bool) {
	if !IsAccess(p.Type) {
		return 0, false
	}
	return accessTariff(p)
}

// Access is an access to a tariff a code gave: whose, which tariff, and until when.
type Access struct {
	ID       int64
	PromoID  int64
	TariffID int64
	TgID     int64
	UserID   int64
	// GrantedAt is when the code was redeemed.
	GrantedAt time.Time
	// ExpiresAt is zero when the access never ends.
	ExpiresAt time.Time
	// Code and Mode are the code that gave it and the code's type (access_paid, access_free).
	Code string
	Mode string
}

func accessOf(a db.PromoAccess, code, mode string) Access {
	out := Access{ID: a.ID, PromoID: a.PromoID, TariffID: a.TariffID, TgID: a.TgID, GrantedAt: time.Unix(a.GrantedAt, 0), Code: code, Mode: mode}
	if a.UserID.Valid {
		out.UserID = a.UserID.Int64
	}
	if a.ExpiresAt.Valid {
		out.ExpiresAt = time.Unix(a.ExpiresAt.Int64, 0)
	}
	return out
}

// GrantAccess records the account's right to use an access code and its tariff, once. The
// code's own rules are checked (window, uses left, per-account limit): the caller gets the
// grant it may use, or the refusal. A code already redeemed is the same grant, not another
// use, so a second tap, the bot and the Mini App at once, or a retry after a failure all end
// the same way.
func (s *Service) GrantAccess(ctx context.Context, tgID, userID int64, code string) (Access, error) {
	var out Access
	err := s.Store.Tx(ctx, func(q *db.Queries) error {
		out = Access{}
		p, err := q.GetPromoCodeByCode(ctx, Normalize(code))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		tariffID, ok := TariffOf(p)
		if !ok {
			// Not an access code, or one whose tariff is missing: it opens nothing either way.
			return ErrNotAccess
		}
		if err := active(p, s.Now()); err != nil {
			return err
		}
		now := s.Now().Unix()
		// The account's own grant comes before the rules that count uses.
		if a, err := q.GetPromoAccess(ctx, db.GetPromoAccessParams{PromoID: p.ID, TgID: tgID, TariffID: tariffID, Now: now}); err == nil {
			out = accessOf(a, p.Code, p.Type)
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := s.checkUser(ctx, q, p, tgID, userID, s.Now()); err != nil {
			return err
		}
		n, err := q.IncrementPromoUse(ctx, p.ID)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrLimit
		}
		expires := int64(0)
		if p.AccessTtl > 0 {
			expires = s.Now().Add(time.Duration(p.AccessTtl) * time.Second).Unix()
		}
		a, err := q.CreatePromoAccess(ctx, db.CreatePromoAccessParams{
			PromoID: p.ID, TariffID: tariffID, UserID: sql.NullInt64{Int64: userID, Valid: userID != 0}, TgID: tgID,
			GrantedAt: now, ExpiresAt: expires, Note: p.Description,
		})
		if err != nil {
			// A redemption that raced this one is the same grant, not a failure.
			if a2, e2 := q.GetPromoAccess(ctx, db.GetPromoAccessParams{PromoID: p.ID, TgID: tgID, TariffID: tariffID, Now: now}); e2 == nil {
				out = accessOf(a2, p.Code, p.Type)
				return nil
			}
			return err
		}
		out = accessOf(a, p.Code, p.Type)
		return nil
	})
	if err == nil && s.Changed != nil {
		s.Changed()
	}
	return out, err
}

// HasAccess: the Telegram account may use tariffID now, because a code it redeemed opens it.
func (s *Service) HasAccess(ctx context.Context, tgID, tariffID int64) (bool, error) {
	if tgID == 0 || tariffID == 0 {
		return false, nil
	}
	return s.Store.Q.HasPromoAccess(ctx, db.HasPromoAccessParams{TgID: tgID, TariffID: tariffID, Now: s.Now().Unix()})
}

// AccessTariffs is the tariffs the Telegram account's codes open right now.
func (s *Service) AccessTariffs(ctx context.Context, tgID int64) ([]int64, error) {
	if tgID == 0 {
		return nil, nil
	}
	return s.Store.Q.ListPromoAccessTariffs(ctx, db.ListPromoAccessTariffsParams{TgID: tgID, Now: s.Now().Unix()})
}

// AccessOf is the account's access to one tariff with the code that gave it; ok false when
// there is none or the code no longer stands.
func (s *Service) AccessOf(ctx context.Context, tgID, tariffID int64) (Access, bool, error) {
	if tgID == 0 || tariffID == 0 {
		return Access{}, false, nil
	}
	rows, err := s.Store.Q.ListPromoAccess(ctx, db.ListPromoAccessParams{TgID: tgID, Now: s.Now().Unix()})
	if err != nil {
		return Access{}, false, err
	}
	for _, row := range rows {
		if row.TariffID == tariffID {
			return accessOf(db.PromoAccess{ID: row.ID, PromoID: row.PromoID, TariffID: row.TariffID, UserID: row.UserID, TgID: row.TgID,
				GrantedAt: row.GrantedAt, ExpiresAt: row.ExpiresAt, Note: row.Note}, row.Code, row.Type), true, nil
		}
	}
	return Access{}, false, nil
}

// Accesses lists the account's live accesses, the latest first (the Mini App shows which
// tariff a code opened).
func (s *Service) Accesses(ctx context.Context, tgID int64) ([]Access, error) {
	if tgID == 0 {
		return nil, nil
	}
	rows, err := s.Store.Q.ListPromoAccess(ctx, db.ListPromoAccessParams{TgID: tgID, Now: s.Now().Unix()})
	if err != nil {
		return nil, err
	}
	out := make([]Access, 0, len(rows))
	for _, row := range rows {
		out = append(out, accessOf(db.PromoAccess{ID: row.ID, PromoID: row.PromoID, TariffID: row.TariffID, UserID: row.UserID, TgID: row.TgID,
			GrantedAt: row.GrantedAt, ExpiresAt: row.ExpiresAt, Note: row.Note}, row.Code, row.Type))
	}
	return out, nil
}

// SetAccessUser names the subscription an access gave its tariff to, so the admin's list
// shows which account the code reached.
func (s *Service) SetAccessUser(ctx context.Context, accessID, userID int64) error {
	if accessID == 0 || userID == 0 {
		return nil
	}
	return s.Store.Q.SetPromoAccessUser(ctx, db.SetPromoAccessUserParams{ID: accessID, UserID: sql.NullInt64{Int64: userID, Valid: true}})
}
