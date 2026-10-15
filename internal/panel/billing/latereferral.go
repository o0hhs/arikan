package billing

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"mikan/internal/panel/store/db"
)

// A payment a provider reports paid after its referral balance hold was given back (the
// invoice expired, the reconcile pass released the hold) is not applied: the hold is the
// buyer's money again, so the subscription would cost less than the tariff and the panel
// would pay the difference out of its own pocket. Its money goes back to the buyer
// instead, exactly as for a late promo payment (latepromo.go).

// lateReferralRefundError marks a payment whose late-referral refund has to be retried:
// ListLateReferralRefunds finds them, and the admin sees why on the Payments page.
const lateReferralRefundError = "ref_late_refund_failed"

// refundLateReferralPayment gives back the money of a payment whose balance hold was
// released, and says whether it did: false means the hold still stands and the payment is
// applied as usual. The refund carries a key per payment and reason, so a retry after a
// restart does not send the money back twice.
func (s *Service) refundLateReferralPayment(ctx context.Context, paymentID int64, externalID string) (bool, error) {
	if s.d.Referral == nil {
		return false, nil
	}
	unlock := s.lockLateRefund(paymentID)
	defer unlock()

	pay, err := s.d.Store.Q.GetPayment(ctx, paymentID)
	if err != nil {
		return false, err
	}
	if pay.Status == PaymentRefunded {
		return true, nil
	}
	released, err := s.d.Referral.Released(ctx, paymentID)
	if err != nil {
		return false, err
	}
	if !released {
		return false, nil
	}
	if externalID != "" && pay.ExternalID.String != externalID {
		if err := s.d.Store.Q.SetPaymentExternalID(ctx, db.SetPaymentExternalIDParams{ExternalID: sql.NullString{String: externalID, Valid: true}, ID: paymentID}); err != nil {
			return true, s.markLateReferralRefundFailed(ctx, paymentID, err)
		}
		pay.ExternalID = sql.NullString{String: externalID, Valid: true}
	}
	if !pay.ExternalID.Valid || pay.ExternalID.String == "" {
		// Nothing to refund by: the charge the provider reported is not known here, so the
		// next report of it finishes the refund.
		return true, s.markLateReferralRefundFailed(ctx, paymentID, errors.New("no charge to refund"))
	}
	if err := s.refundCharge(ctx, pay, pay.ExternalID.String, "mikan-ref-late-"+strconv.FormatInt(pay.ID, 10)); err != nil {
		return true, s.markLateReferralRefundFailed(ctx, paymentID, err)
	}
	// The same statement an open refund of the admin's or a late promo payment uses: the
	// payment goes to refunded and the error clears.
	if _, err := s.d.Store.Q.MarkLatePromoRefunded(ctx, db.MarkLatePromoRefundedParams{RefundedAt: sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true}, ID: paymentID}); err != nil {
		return true, s.markLateReferralRefundFailed(ctx, paymentID, err)
	}
	// The payment will never be applied, so a code reserved for it is free again.
	if s.d.Promo != nil {
		if err := s.d.Promo.ReleasePayment(ctx, paymentID); err != nil {
			s.d.Log.Warn("billing: release promo of a late referral refund", "payment", paymentID, "err", err)
		}
	}
	s.d.Log.Warn("billing: payment made after its referral hold was released was refunded", "payment", paymentID, "provider", pay.Provider)
	return true, nil
}

func (s *Service) markLateReferralRefundFailed(ctx context.Context, paymentID int64, cause error) error {
	if err := s.d.Store.Q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: lateReferralRefundError, ID: paymentID}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
