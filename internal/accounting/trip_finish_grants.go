package accounting

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"taxi-mvp/internal/domain"
)

// GrantTripFinishPromosAndReferral runs first-3-trip promo and referral reward in a single DB transaction
// after the trip row is already FINISHED (separate commit from TripRepo.UpdateToFinished).
func GrantTripFinishPromosAndReferral(ctx context.Context, db *sql.DB, tripDriverUserID int64, tripID string) (firstThreeGranted bool, firstThreeTripNum int, ref ReferralRewardResult, err error) {
	tripID = strings.TrimSpace(tripID)
	if tripID == "" {
		return false, 0, ReferralRewardResult{Reason: ReferralRewardReasonEmptyTripID}, ErrEmptyTripID
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, ReferralRewardResult{Reason: ReferralRewardReasonDBError}, err
	}
	defer func() { _ = tx.Rollback() }()

	var rowDriver int64
	var st string
	err = tx.QueryRowContext(ctx, `SELECT driver_user_id, status FROM trips WHERE id = ?1`, tripID).Scan(&rowDriver, &st)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("TRIP_FINISH_GRANT_FAIL trip_id=%s driver_user_id=%d reason=trip_not_found", tripID, tripDriverUserID)
			return false, 0, ReferralRewardResult{Reason: ReferralRewardReasonDBError}, err
		}
		return false, 0, ReferralRewardResult{Reason: ReferralRewardReasonDBError}, err
	}
	if rowDriver != tripDriverUserID {
		log.Printf("TRIP_FINISH_GRANT_MISMATCH trip_id=%s auth_driver_user_id=%d trip_row_driver_user_id=%d", tripID, tripDriverUserID, rowDriver)
		return false, 0, ReferralRewardResult{Reason: ReferralRewardReasonDBError}, fmt.Errorf("trip driver mismatch")
	}
	if st != domain.TripStatusFinished {
		log.Printf("TRIP_FINISH_GRANT_FAIL trip_id=%s driver_user_id=%d status=%s want=%s", tripID, tripDriverUserID, st, domain.TripStatusFinished)
		return false, 0, ReferralRewardResult{Reason: ReferralRewardReasonDBError}, fmt.Errorf("trip not FINISHED")
	}

	referredDriverUserID := tripDriverUserID
	log.Printf("TRIP_FINISH_GRANTS_BEGIN trip_id=%s trip_driver_user_id=%d referral_user_id=%d", tripID, tripDriverUserID, referredDriverUserID)

	firstThreeGranted, firstThreeTripNum, err = grantFirstThreeTripPromoInTx(ctx, tx, db, tripDriverUserID, tripID)
	if err != nil {
		return false, 0, ReferralRewardResult{Reason: ReferralRewardReasonDBError}, err
	}
	ref, err = grantReferralRewardInTx(ctx, tx, db, referredDriverUserID, tripID)
	if err != nil {
		return firstThreeGranted, firstThreeTripNum, ref, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, ReferralRewardResult{Reason: ReferralRewardReasonDBError}, err
	}
	return firstThreeGranted, firstThreeTripNum, ref, nil
}
