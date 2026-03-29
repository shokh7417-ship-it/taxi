package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"taxi-mvp/internal/domain"

	_ "modernc.org/sqlite"
)

func setupAccountingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:promo_referral_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		telegram_id INTEGER NOT NULL DEFAULT 0,
		referral_code TEXT,
		referred_by TEXT,
		referral_stage2_reward_paid INTEGER NOT NULL DEFAULT 0
	);`)
	exec(`CREATE TABLE drivers (
		user_id INTEGER PRIMARY KEY,
		promo_balance INTEGER NOT NULL DEFAULT 0,
		balance INTEGER NOT NULL DEFAULT 0,
		verification_status TEXT NOT NULL DEFAULT 'approved',
		signup_bonus_paid INTEGER NOT NULL DEFAULT 0
	);`)
	exec(`CREATE TABLE driver_referrals (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		inviter_user_id INTEGER NOT NULL,
		referred_user_id INTEGER NOT NULL UNIQUE,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);`)
	exec(`CREATE TABLE trips (
		id TEXT PRIMARY KEY,
		driver_user_id INTEGER NOT NULL,
		rider_user_id INTEGER NOT NULL,
		status TEXT NOT NULL
	);`)
	exec(`CREATE TABLE driver_ledger (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		driver_id INTEGER NOT NULL,
		bucket TEXT NOT NULL,
		entry_type TEXT NOT NULL,
		amount INTEGER NOT NULL,
		reference_type TEXT,
		reference_id TEXT,
		note TEXT,
		metadata_json TEXT,
		expires_at TEXT,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);`)
	exec(`CREATE UNIQUE INDEX idx_first3 ON driver_ledger(driver_id, reference_id)
		WHERE reference_type = 'first_3_trip_bonus' AND entry_type = 'PROMO_GRANTED';`)
	exec(`CREATE UNIQUE INDEX idx_ref ON driver_ledger(driver_id, reference_id)
		WHERE reference_type = 'referral_reward' AND entry_type = 'PROMO_GRANTED';`)
	return db
}

func TestTryGrantSignupPromoOnce_Idempotent(t *testing.T) {
	db := setupAccountingTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO users (id) VALUES (1)`)
	_, _ = db.Exec(`INSERT INTO drivers (user_id) VALUES (1)`)
	if err := TryGrantSignupPromoOnce(ctx, db, 1); err != nil {
		t.Fatal(err)
	}
	var bal int
	_ = db.QueryRow(`SELECT promo_balance FROM drivers WHERE user_id=1`).Scan(&bal)
	if bal != int(DriverSignupPromoSoM) {
		t.Fatalf("promo balance want %d got %d", DriverSignupPromoSoM, bal)
	}
	if err := TryGrantSignupPromoOnce(ctx, db, 1); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT promo_balance FROM drivers WHERE user_id=1`).Scan(&bal)
	if bal != int(DriverSignupPromoSoM) {
		t.Fatalf("second grant should not double: got %d", bal)
	}
}

func TestTryGrantFirstThreeTripPromo_SequentialAndFourth(t *testing.T) {
	db := setupAccountingTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO users (id) VALUES (1)`)
	_, _ = db.Exec(`INSERT INTO drivers (user_id) VALUES (1)`)
	rider := int64(2)
	insertTrip := func(id string) {
		_, err := db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES (?1, 1, ?2, ?3)`, id, rider, domain.TripStatusFinished)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertTrip("t1")
	g, n, err := TryGrantFirstThreeTripPromo(ctx, db, 1, "t1")
	if err != nil || !g || n != 1 {
		t.Fatalf("trip1 want granted num=1 got granted=%v num=%v err=%v", g, n, err)
	}
	insertTrip("t2")
	g, n, err = TryGrantFirstThreeTripPromo(ctx, db, 1, "t2")
	if err != nil || !g || n != 2 {
		t.Fatalf("trip2 want granted num=2 got granted=%v num=%v err=%v", g, n, err)
	}
	insertTrip("t3")
	g, n, err = TryGrantFirstThreeTripPromo(ctx, db, 1, "t3")
	if err != nil || !g || n != 3 {
		t.Fatalf("trip3 want granted num=3 got granted=%v num=%v err=%v", g, n, err)
	}
	insertTrip("t4")
	g, n, err = TryGrantFirstThreeTripPromo(ctx, db, 1, "t4")
	if err != nil || g {
		t.Fatalf("trip4 want no grant got granted=%v num=%v err=%v", g, n, err)
	}
	var bal int
	_ = db.QueryRow(`SELECT promo_balance FROM drivers WHERE user_id=1`).Scan(&bal)
	want := int(FirstThreeTripPromoSoM * 3)
	if bal != want {
		t.Fatalf("promo want %d got %d", want, bal)
	}
}

func TestTryGrantFirstThreeTripPromo_DuplicateFinishNoop(t *testing.T) {
	db := setupAccountingTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO users (id) VALUES (1)`)
	_, _ = db.Exec(`INSERT INTO drivers (user_id) VALUES (1)`)
	_, _ = db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES ('t1', 1, 2, ?1)`, domain.TripStatusFinished)
	g, _, err := TryGrantFirstThreeTripPromo(ctx, db, 1, "t1")
	if err != nil || !g {
		t.Fatalf("first grant: %v %v", g, err)
	}
	g, _, err = TryGrantFirstThreeTripPromo(ctx, db, 1, "t1")
	if err != nil || g {
		t.Fatalf("duplicate want noop got granted=%v err=%v", g, err)
	}
}

func TestTryGrantFirstThreeTripPromo_MetadataJSON(t *testing.T) {
	db := setupAccountingTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO users (id) VALUES (1)`)
	_, _ = db.Exec(`INSERT INTO drivers (user_id) VALUES (1)`)
	_, _ = db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES ('t1', 1, 2, ?1)`, domain.TripStatusFinished)
	_, _, err := TryGrantFirstThreeTripPromo(ctx, db, 1, "t1")
	if err != nil {
		t.Fatal(err)
	}
	var meta string
	_ = db.QueryRow(`SELECT metadata_json FROM driver_ledger WHERE reference_type=?1`, RefTypeFirst3TripBonus).Scan(&meta)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		t.Fatal(err)
	}
	if m["source"] != "first_3_trip_bonus" || m["program"] != promoProgramID {
		t.Fatalf("metadata wrong: %#v", m)
	}
}

func TestTryGrantReferralReward_NotUntilThirdTrip(t *testing.T) {
	db := setupAccountingTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO users (id, referral_code) VALUES (10, 'inv1')`)
	_, _ = db.Exec(`INSERT INTO users (id, referred_by) VALUES (11, 'inv1')`)
	_, _ = db.Exec(`INSERT INTO drivers (user_id) VALUES (10)`)
	_, _ = db.Exec(`INSERT INTO drivers (user_id) VALUES (11)`)

	addFinished := func(tid string) {
		_, err := db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES (?1, 11, 20, ?2)`, tid, domain.TripStatusFinished)
		if err != nil {
			t.Fatal(err)
		}
	}
	addFinished("a")
	r, err := TryGrantReferralReward(ctx, db, 11, "a")
	if err != nil || r.Granted || r.Reason != ReferralRewardReasonNotEnoughTrips {
		t.Fatalf("after 1: %+v err=%v", r, err)
	}
	addFinished("b")
	r, err = TryGrantReferralReward(ctx, db, 11, "b")
	if err != nil || r.Granted || r.Reason != ReferralRewardReasonNotEnoughTrips {
		t.Fatalf("after 2: %+v err=%v", r, err)
	}
	addFinished("c")
	r, err = TryGrantReferralReward(ctx, db, 11, "c")
	if err != nil || !r.Granted || r.Reason != ReferralRewardReasonSuccess {
		t.Fatalf("after 3: %+v err=%v", r, err)
	}
	var invPromo int
	_ = db.QueryRow(`SELECT promo_balance FROM drivers WHERE user_id=10`).Scan(&invPromo)
	if invPromo != int(ReferralRewardPromoSoM) {
		t.Fatalf("inviter promo want %d got %d", ReferralRewardPromoSoM, invPromo)
	}
	r, err = TryGrantReferralReward(ctx, db, 11, "c")
	if err != nil || r.Granted || r.Reason != ReferralRewardReasonAlreadyGranted {
		t.Fatalf("duplicate trip3 finish want already_granted: %+v", r)
	}
	addFinished("d")
	r, err = TryGrantReferralReward(ctx, db, 11, "d")
	if err != nil || r.Granted || r.Reason != ReferralRewardReasonPastThirdTrip {
		t.Fatalf("after 4 want past_third_trip skip: %+v err=%v", r, err)
	}
}

func TestTryGrantReferralReward_EmptyTripIDSkips(t *testing.T) {
	db := setupAccountingTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO users (id) VALUES (11)`)
	_, _ = db.Exec(`INSERT INTO drivers (user_id) VALUES (11)`)
	r, err := TryGrantReferralReward(ctx, db, 11, "")
	if err != nil || r.Granted || r.Reason != ReferralRewardReasonEmptyTripID {
		t.Fatalf("empty trip id: %+v err=%v", r, err)
	}
}

func TestFinishedTripCountAfterCompletingTrip_IncludesTripAndRequiresFinished(t *testing.T) {
	db := setupAccountingTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES ('a', 1, 2, ?1)`, domain.TripStatusFinished)
	_, _ = db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES ('b', 1, 2, ?1)`, domain.TripStatusFinished)
	n, err := FinishedTripCountAfterCompletingTrip(ctx, db, 1, "b")
	if err != nil || n != 2 {
		t.Fatalf("count want 2 got %d err=%v", n, err)
	}
	_, _ = db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES ('c', 1, 2, ?1)`, domain.TripStatusStarted)
	_, err = FinishedTripCountAfterCompletingTrip(ctx, db, 1, "c")
	if err == nil {
		t.Fatal("want error when trip not FINISHED")
	}
}

func TestCountFinishedTripsForDriver_ExcludesNonFinished(t *testing.T) {
	db := setupAccountingTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES ('a', 1, 2, ?1)`, domain.TripStatusFinished)
	_, _ = db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES ('b', 1, 2, ?1)`, domain.TripStatusStarted)
	_, _ = db.Exec(`INSERT INTO trips (id, driver_user_id, rider_user_id, status) VALUES ('c', 1, 2, ?1)`, "CANCELLED_BY_RIDER")
	n, err := CountFinishedTripsForDriver(ctx, db, 1)
	if err != nil || n != 1 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}
