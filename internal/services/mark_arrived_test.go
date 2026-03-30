package services

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/logger"
	"taxi-mvp/internal/repositories"

	_ "modernc.org/sqlite"
)

type sentTelegramMsg struct {
	chatID int64
	text   string
}

type fakeTelegramBot struct {
	mu      sync.Mutex
	sent    []sentTelegramMsg
	sendErr error
}

func (f *fakeTelegramBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	if f.sendErr != nil {
		return tgbotapi.Message{}, f.sendErr
	}
	switch m := c.(type) {
	case tgbotapi.MessageConfig:
		f.mu.Lock()
		f.sent = append(f.sent, sentTelegramMsg{chatID: m.ChatID, text: m.Text})
		f.mu.Unlock()
		return tgbotapi.Message{}, nil
	default:
		return tgbotapi.Message{}, fmt.Errorf("unexpected chattable type %T", c)
	}
}

func (f *fakeTelegramBot) messagesTo(chatID int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sent {
		if s.chatID == chatID {
			out = append(out, s.text)
		}
	}
	return out
}

func setupMarkArrivedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:mark_arrived?mode=memory&cache=shared")
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
		telegram_id INTEGER NOT NULL DEFAULT 0
	);`)
	exec(`CREATE TABLE ride_requests (
		id TEXT PRIMARY KEY,
		pickup_lat REAL NOT NULL,
		pickup_lng REAL NOT NULL
	);`)
	exec(`CREATE TABLE trips (
		id TEXT PRIMARY KEY,
		request_id TEXT NOT NULL,
		driver_user_id INTEGER NOT NULL,
		rider_user_id INTEGER NOT NULL,
		status TEXT NOT NULL,
		arrived_at TEXT
	);`)
	exec(`CREATE TABLE drivers (
		user_id INTEGER PRIMARY KEY,
		last_lat REAL,
		last_lng REAL,
		last_live_location_at TEXT,
		live_location_active INTEGER NOT NULL DEFAULT 0
	);`)
	return db
}

const (
	testRiderText = "✅ Haydovchi sizning manzilingizga yetib keldi.\n\nSafar boshlashga tayyor: haydovchi bilan uchrashing. Haydovchi safarni boshlagach, yo‘l davom etadi."
	testDriverText = "✅ Mijozga yetib keldingiz. Yo‘lovchiga xabar yuborildi. Safarni boshlashingiz mumkin."
)

// pickLat/pickLng — driver same coords, fresh live location within 90s.
func seedTripWaitingNearPickup(t *testing.T, db *sql.DB, tripID, reqID string, driverID, riderID, riderTg, driverTg int64, pickLat, pickLng float64, liveAt time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO users (id, telegram_id) VALUES (?1, ?2), (?3, ?4)`,
		riderID, riderTg, driverID, driverTg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO ride_requests (id, pickup_lat, pickup_lng) VALUES (?1, ?2, ?3)`, reqID, pickLat, pickLng)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO trips (id, request_id, driver_user_id, rider_user_id, status) VALUES (?1, ?2, ?3, ?4, ?5)`,
		tripID, reqID, driverID, riderID, domain.TripStatusWaiting)
	if err != nil {
		t.Fatal(err)
	}
	liveStr := liveAt.UTC().Format("2006-01-02 15:04:05")
	_, err = db.Exec(`INSERT INTO drivers (user_id, last_lat, last_lng, last_live_location_at, live_location_active) VALUES (?1, ?2, ?3, ?4, 1)`,
		driverID, pickLat, pickLng, liveStr)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMarkArrived_SuccessSendsRiderAndDriverTelegram(t *testing.T) {
	db := setupMarkArrivedTestDB(t)
	defer db.Close()
	tripID := "trip-a"
	reqID := "req-a"
	const driverID int64 = 10
	const riderID int64 = 11
	const riderTg int64 = 1001
	const driverTg int64 = 2002
	pickLat, pickLng := 40.23, 68.843
	seedTripWaitingNearPickup(t, db, tripID, reqID, driverID, riderID, riderTg, driverTg, pickLat, pickLng, time.Now().UTC())

	riderBot := &fakeTelegramBot{}
	driverBot := &fakeTelegramBot{}
	cfg := &config.Config{PickupStartMaxMeters: 500}
	svc := NewTripService(db, repositories.NewTripRepo(db), riderBot, driverBot, cfg, nil, nil, nil)

	res, err := svc.MarkArrived(context.Background(), tripID, driverID)
	if err != nil {
		t.Fatalf("MarkArrived: %v", err)
	}
	if res == nil || res.Result != "updated" || res.Status != domain.TripStatusArrived {
		t.Fatalf("result: %+v", res)
	}

	riderMsgs := riderBot.messagesTo(riderTg)
	if len(riderMsgs) != 1 || riderMsgs[0] != testRiderText {
		t.Fatalf("rider messages: %q want %q", riderMsgs, testRiderText)
	}
	driverMsgs := driverBot.messagesTo(driverTg)
	if len(driverMsgs) != 1 || driverMsgs[0] != testDriverText {
		t.Fatalf("driver messages: %q want %q", driverMsgs, testDriverText)
	}
}

func TestMarkArrived_PickupGuardFails_NoRiderNotification(t *testing.T) {
	db := setupMarkArrivedTestDB(t)
	defer db.Close()
	tripID := "trip-b"
	reqID := "req-b"
	const driverID int64 = 10
	const riderID int64 = 11
	const riderTg int64 = 1001
	const driverTg int64 = 2002
	pickLat, pickLng := 40.23, 68.843
	// Driver "far" from pickup (~2+ km north)
	farLat := pickLat + 0.025
	seedTripWaitingNearPickup(t, db, tripID, reqID, driverID, riderID, riderTg, driverTg, pickLat, pickLng, time.Now().UTC())
	_, _ = db.Exec(`UPDATE drivers SET last_lat = ?1, last_lng = ?2 WHERE user_id = ?3`, farLat, pickLng, driverID)

	riderBot := &fakeTelegramBot{}
	driverBot := &fakeTelegramBot{}
	cfg := &config.Config{PickupStartMaxMeters: 100}
	svc := NewTripService(db, repositories.NewTripRepo(db), riderBot, driverBot, cfg, nil, nil, nil)

	_, err := svc.MarkArrived(context.Background(), tripID, driverID)
	if err == nil {
		t.Fatal("expected error (too far from pickup)")
	}
	if !errors.Is(err, domain.ErrTooFarFromPickup) {
		t.Fatalf("want ErrTooFarFromPickup, got %v", err)
	}
	if len(riderBot.sent) != 0 || len(driverBot.sent) != 0 {
		t.Fatalf("no telegram sends on failure; rider=%d driver=%d", len(riderBot.sent), len(driverBot.sent))
	}
}

func TestMarkArrived_LogsArrivedNotifySummary(t *testing.T) {
	db := setupMarkArrivedTestDB(t)
	defer db.Close()
	tripID := "trip-c"
	reqID := "req-c"
	const driverID int64 = 10
	const riderID int64 = 11
	const riderTg int64 = 1001
	const driverTg int64 = 2002
	pickLat, pickLng := 41.0, 69.0
	seedTripWaitingNearPickup(t, db, tripID, reqID, driverID, riderID, riderTg, driverTg, pickLat, pickLng, time.Now().UTC())

	var buf bytes.Buffer
	oldLog := logger.Log
	logger.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	defer func() { logger.Log = oldLog }()

	riderBot := &fakeTelegramBot{}
	driverBot := &fakeTelegramBot{}
	cfg := &config.Config{PickupStartMaxMeters: 500}
	svc := NewTripService(db, repositories.NewTripRepo(db), riderBot, driverBot, cfg, nil, nil, nil)

	if _, err := svc.MarkArrived(context.Background(), tripID, driverID); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !containsAll(out, "arrived_notify_summary", "arrived_notify_driver_sent") {
		t.Fatalf("log output missing expected events; got:\n%s", out)
	}
}

// flakyRiderBot fails Send for the first n attempts, then succeeds.
type flakyRiderBot struct {
	mu           sync.Mutex
	failRemaining int
	attempts      int
	fakeTelegramBot
}

func (f *flakyRiderBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.mu.Lock()
	f.attempts++
	fail := f.failRemaining
	if f.failRemaining > 0 {
		f.failRemaining--
	}
	f.mu.Unlock()
	if fail > 0 {
		return tgbotapi.Message{}, fmt.Errorf("telegram temporary error")
	}
	return f.fakeTelegramBot.Send(c)
}

func TestMarkArrived_RiderNotifyRetriesThenSucceeds(t *testing.T) {
	db := setupMarkArrivedTestDB(t)
	defer db.Close()
	tripID := "trip-retry"
	reqID := "req-retry"
	const driverID int64 = 10
	const riderID int64 = 11
	const riderTg int64 = 1001
	const driverTg int64 = 2002
	pickLat, pickLng := 40.23, 68.843
	seedTripWaitingNearPickup(t, db, tripID, reqID, driverID, riderID, riderTg, driverTg, pickLat, pickLng, time.Now().UTC())

	riderBot := &flakyRiderBot{failRemaining: 2}
	driverBot := &fakeTelegramBot{}
	cfg := &config.Config{PickupStartMaxMeters: 500}
	svc := NewTripService(db, repositories.NewTripRepo(db), riderBot, driverBot, cfg, nil, nil, nil)

	_, err := svc.MarkArrived(context.Background(), tripID, driverID)
	if err != nil {
		t.Fatalf("MarkArrived: %v", err)
	}
	riderBot.mu.Lock()
	att := riderBot.attempts
	riderBot.mu.Unlock()
	if att != arrivedRiderNotifyMaxAttempts {
		t.Fatalf("rider send attempts: got %d want %d", att, arrivedRiderNotifyMaxAttempts)
	}
	riderMsgs := riderBot.messagesTo(riderTg)
	if len(riderMsgs) != 1 || riderMsgs[0] != testRiderText {
		t.Fatalf("rider messages: %q want one with body %q", riderMsgs, testRiderText)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !bytes.Contains([]byte(s), []byte(sub)) {
			return false
		}
	}
	return true
}