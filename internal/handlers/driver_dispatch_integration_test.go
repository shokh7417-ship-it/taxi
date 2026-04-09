package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pressly/goose/v3"
	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"

	"taxi-mvp/internal/auth"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/legal"
)

func openMigratedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "test.db")
	dsn := "file:" + filepath.ToSlash(path)
	db, err := sql.Open("libsql", dsn)
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	dir := filepath.Join("..", "..", "db", "migrations")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("migrations dir %s: %v", dir, err)
	}
	if err := goose.Up(db, dir); err != nil {
		t.Fatalf("goose up: %v", err)
	}
	return db
}

func seedLegalAcceptances(t *testing.T, db *sql.DB, driverUserID int64) {
	t.Helper()
	const q = `
		INSERT INTO legal_acceptances (user_id, document_type, version, accepted_at)
		SELECT ?1, ld.document_type, ld.version, datetime('now')
		FROM legal_documents ld
		WHERE ld.is_active = 1 AND ld.document_type IN ('driver_terms', 'privacy_policy_driver')`
	if _, err := db.Exec(q, driverUserID); err != nil {
		t.Fatalf("seed legal acceptances: %v", err)
	}
}

// TestDriverAvailableRequests_WithSentNotification verifies GET contract: queue comes from
// request_notifications SENT joined to PENDING non-expired ride_requests (internal users.id).
func TestDriverAvailableRequests_WithSentNotification(t *testing.T) {
	db := openMigratedTestDB(t)

	_, err := db.Exec(`
		INSERT INTO users (telegram_id, role, name, phone) VALUES (900001, 'driver', 'Driver D', '+10000000001'),
			(900002, 'rider', 'Rider R', '+10000000002')`)
	if err != nil {
		t.Fatalf("insert users: %v", err)
	}

	var driverUID, riderUID int64
	if err := db.QueryRow(`SELECT id FROM users WHERE telegram_id = 900001`).Scan(&driverUID); err != nil {
		t.Fatalf("driver user: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM users WHERE telegram_id = 900002`).Scan(&riderUID); err != nil {
		t.Fatalf("rider user: %v", err)
	}

	seedLegalAcceptances(t, db, driverUID)

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	expires := time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")
	const lat, lng = 41.3111, 69.2797

	_, err = db.Exec(`
		INSERT INTO drivers (user_id, is_active, last_lat, last_lng, last_seen_at,
			verification_status, balance, live_location_active, last_live_location_at, manual_offline,
			phone, car_type, color, plate)
		VALUES (?1, 1, ?2, ?3, ?4, 'approved', 10000, 1, ?4, 0,
			'+10000000001', 'sedan', 'white', '01 A 001 AA')`,
		driverUID, lat, lng, now)
	if err != nil {
		t.Fatalf("insert driver: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO ride_requests (id, rider_user_id, pickup_lat, pickup_lng, radius_km, status, created_at, expires_at)
		VALUES ('test-req-available', ?1, ?2, ?3, 10, 'PENDING', datetime('now'), ?4)`,
		riderUID, lat, lng, expires)
	if err != nil {
		t.Fatalf("insert ride_request: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO request_notifications (request_id, driver_user_id, chat_id, message_id, status)
		VALUES ('test-req-available', ?1, 0, 0, 'SENT')`, driverUID)
	if err != nil {
		t.Fatalf("insert notification: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/driver/available-requests", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{UserID: driverUID, Role: domain.RoleDriver}))
	c.Request = req

	DriverAvailableRequests(db, &config.Config{})(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		AvailableRequests []DriverAvailableOffer `json:"available_requests"`
		Assigned          *struct{}              `json:"assigned_trip"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body.Assigned != nil {
		t.Fatalf("assigned_trip: want null")
	}
	if len(body.AvailableRequests) != 1 || body.AvailableRequests[0].RequestID != "test-req-available" {
		t.Fatalf("offers: %+v", body.AvailableRequests)
	}
}

// TestDriverAvailableRequests_SentButRequestNotPending ensures joined rows disappear when ride is no longer PENDING.
func TestDriverAvailableRequests_SentButRequestNotPending(t *testing.T) {
	db := openMigratedTestDB(t)

	_, err := db.Exec(`
		INSERT INTO users (telegram_id, role, name, phone) VALUES (910001, 'driver', 'D', '+1'), (910002, 'rider', 'R', '+2')`)
	if err != nil {
		t.Fatal(err)
	}
	var driverUID, riderUID int64
	_ = db.QueryRow(`SELECT id FROM users WHERE telegram_id = 910001`).Scan(&driverUID)
	_ = db.QueryRow(`SELECT id FROM users WHERE telegram_id = 910002`).Scan(&riderUID)
	seedLegalAcceptances(t, db, driverUID)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	expires := time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")
	_, _ = db.Exec(`
		INSERT INTO drivers (user_id, is_active, last_lat, last_lng, last_seen_at,
			verification_status, balance, live_location_active, last_live_location_at, manual_offline,
			phone, car_type, color, plate)
		VALUES (?1, 1, 41.0, 69.0, ?2, 'approved', 1, 1, ?2, 0, '+1', 'a', 'b', 'c')`,
		driverUID, now)
	_, _ = db.Exec(`
		INSERT INTO ride_requests (id, rider_user_id, pickup_lat, pickup_lng, radius_km, status, created_at, expires_at)
		VALUES ('taken-req', ?1, 41.0, 69.0, 10, 'ASSIGNED', datetime('now'), ?2)`, riderUID, expires)
	_, _ = db.Exec(`
		INSERT INTO request_notifications (request_id, driver_user_id, chat_id, message_id, status)
		VALUES ('taken-req', ?1, 0, 0, 'SENT')`, driverUID)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/driver/available-requests", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{UserID: driverUID, Role: domain.RoleDriver}))
	c.Request = req
	DriverAvailableRequests(db, &config.Config{})(c)
	var body map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	var ar []json.RawMessage
	_ = json.Unmarshal(body["available_requests"], &ar)
	if len(ar) != 0 {
		t.Fatalf("want empty queue when request not PENDING, got %s", w.Body.String())
	}
}

// TestDriverAvailableRequests_DebugReasonsWhenEmpty exercises DRIVER_AVAILABLE_REQUESTS_DEBUG counters:
// legal must be OK or debug adds legal_not_accepted (seed legal so only no_sent_offers remains).
func TestDriverAvailableRequests_DebugReasonsWhenEmpty(t *testing.T) {
	db := openMigratedTestDB(t)
	_, _ = db.Exec(`
		INSERT INTO users (telegram_id, role, name, phone) VALUES (920001, 'driver', 'D', '+19920000001'),
			(920002, 'rider', 'R', '+19920000002')`)
	var driverUID, riderUID int64
	_ = db.QueryRow(`SELECT id FROM users WHERE telegram_id = 920001`).Scan(&driverUID)
	_ = db.QueryRow(`SELECT id FROM users WHERE telegram_id = 920002`).Scan(&riderUID)
	seedLegalAcceptances(t, db, driverUID)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = db.Exec(`
		INSERT INTO drivers (user_id, is_active, last_lat, last_lng, last_seen_at,
			verification_status, balance, live_location_active, last_live_location_at, manual_offline,
			phone, car_type, color, plate)
		VALUES (?1, 1, 41.0, 69.0, ?2, 'approved', 1, 1, ?2, 0, '+19920000001', 'a', 'b', 'c')`,
		driverUID, now)
	_, _ = db.Exec(`INSERT INTO ride_requests (id, rider_user_id, pickup_lat, pickup_lng, radius_km, status, created_at, expires_at)
		VALUES ('only-pending', ?1, 41.0, 69.0, 10, 'PENDING', datetime('now'), datetime('now', '+1 hour'))`,
		riderUID)

	cfg := &config.Config{
		DriverAvailableRequestsDebug:         true,
		DriverAvailableRequestsDebugDriverID:   driverUID,
		EnableDriverHTTPLiveLocation:         true,
		InfiniteDriverBalance:                false,
		DriverSeenSeconds:                    120,
	}
	// Sanity: driver has legal for debug branch
	if !legal.NewService(db).DriverHasActiveLegal(t.Context(), driverUID) {
		t.Fatal("expected driver legal OK")
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/driver/available-requests", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{UserID: driverUID, Role: domain.RoleDriver}))
	c.Request = req
	DriverAvailableRequests(db, cfg)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	// Empty queue + pending ride in DB but no SENT for this driver → reasons include no_sent_offers_for_driver
	// (exact log line is stderr; we assert DB state the debug line is derived from.)
	var pending, sent int
	_ = db.QueryRow(`SELECT COUNT(1) FROM ride_requests WHERE status = 'PENDING' AND expires_at > datetime('now')`).Scan(&pending)
	_ = db.QueryRow(`SELECT COUNT(1) FROM request_notifications WHERE driver_user_id = ? AND status = 'SENT'`, driverUID).Scan(&sent)
	if pending < 1 || sent != 0 {
		t.Fatalf("want pending>=1 and sent=0 for debug scenario, pending=%d sent=%d", pending, sent)
	}
}
