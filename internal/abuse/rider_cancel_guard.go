package abuse

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// RiderPenaltyState describes the current abuse/penalty status for a rider.
type RiderPenaltyState struct {
	Count24h        int
	BlockUntil      *time.Time
	BlockDuration   time.Duration
	EscalationLevel int
	ShouldWarn      bool
}

const (
	// thresholds in a rolling 24h window
	warningMinCount = 1
	warningMaxCount = 2

	block30MinMinCount = 3
	block30MinMaxCount = 4

	block24hMinCount = 5

	// cooldown for repeating warning messages
	warningCooldown = time.Hour
)

// EnsureSchema creates minimal tables for rider abuse tracking if they do not exist.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	// Small, isolated tables; safe to create with IF NOT EXISTS.
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS rider_abuse_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			rider_user_id INTEGER NOT NULL,
			trip_id TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_rider_abuse_events_rider_created_at
			ON rider_abuse_events (rider_user_id, created_at);
		CREATE TABLE IF NOT EXISTS rider_abuse_state (
			rider_user_id INTEGER PRIMARY KEY,
			block_until TEXT,
			last_warning_at TEXT,
			escalation_level INTEGER NOT NULL DEFAULT 0
		);
	`)
	return err
}

// RecordRiderAbuseEvent inserts an abuse event and updates penalty/block state.
// It returns the updated penalty snapshot. Errors are returned but callers should
// treat failures as non-fatal (do not block normal trip flow).
func RecordRiderAbuseEvent(ctx context.Context, db *sql.DB, riderUserID int64, tripID string, now time.Time) (*RiderPenaltyState, error) {
	if riderUserID == 0 || tripID == "" {
		return nil, nil
	}

	if err := insertAbuseEvent(ctx, db, riderUserID, tripID, now); err != nil {
		return nil, err
	}

	count24h, err := countEvents24h(ctx, db, riderUserID, now)
	if err != nil {
		return nil, err
	}

	state, err := loadState(ctx, db, riderUserID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		state = &stateRow{RiderUserID: riderUserID}
	}

	penalty := &RiderPenaltyState{
		Count24h:        count24h,
		BlockDuration:   0,
		EscalationLevel: state.EscalationLevel,
	}

	var existingBlockUntil *time.Time
	if state.BlockUntil.Valid {
		t, perr := parseTime(state.BlockUntil.String)
		if perr == nil {
			existingBlockUntil = &t
		}
	}
	nowUTC := now.UTC()

	// Determine warning.
	if count24h >= warningMinCount && count24h <= warningMaxCount {
		canWarn := true
		if state.LastWarningAt.Valid {
			if t, err := parseTime(state.LastWarningAt.String); err == nil {
				if nowUTC.Sub(t) < warningCooldown {
					canWarn = false
				}
			}
		}
		if canWarn {
			penalty.ShouldWarn = true
			state.LastWarningAt = sql.NullString{String: nowUTC.Format("2006-01-02 15:04:05"), Valid: true}
			log.Printf("rider_abuse: warning rider_user_id=%d count24h=%d", riderUserID, count24h)
		}
	}

	// Determine block duration based on thresholds.
	var newBlockUntil *time.Time
	newEscalation := state.EscalationLevel

	if count24h >= block30MinMinCount && count24h <= block30MinMaxCount {
		// 30-minute temporary block.
		d := 30 * time.Minute
		t := nowUTC.Add(d)
		newBlockUntil = &t
		penalty.BlockDuration = d
		log.Printf("rider_abuse: block_30m rider_user_id=%d count24h=%d", riderUserID, count24h)
	} else if count24h >= block24hMinCount {
		// Escalating blocks: 24h -> 3d -> 7d.
		var d time.Duration
		switch state.EscalationLevel {
		case 0:
			d = 24 * time.Hour
			newEscalation = 1
		case 1:
			d = 3 * 24 * time.Hour
			newEscalation = 2
		default:
			d = 7 * 24 * time.Hour
			newEscalation = 3
		}
		t := nowUTC.Add(d)
		newBlockUntil = &t
		penalty.BlockDuration = d
		log.Printf("rider_abuse: block_escalated rider_user_id=%d count24h=%d level=%d duration=%s", riderUserID, count24h, newEscalation, d)
	}

	// Merge with existing block (keep the furthest in the future).
	effectiveBlockUntil := existingBlockUntil
	if newBlockUntil != nil {
		if effectiveBlockUntil == nil || newBlockUntil.After(*effectiveBlockUntil) {
			effectiveBlockUntil = newBlockUntil
		}
	}

	if effectiveBlockUntil != nil {
		state.BlockUntil = sql.NullString{String: effectiveBlockUntil.Format("2006-01-02 15:04:05"), Valid: true}
		penalty.BlockUntil = effectiveBlockUntil
	}
	state.EscalationLevel = newEscalation
	penalty.EscalationLevel = newEscalation

	if err := upsertState(ctx, db, state); err != nil {
		return nil, err
	}

	// Log final snapshot for audit.
	if penalty.BlockUntil != nil {
		log.Printf("rider_abuse: state_update rider_user_id=%d count24h=%d block_until=%s level=%d", riderUserID, count24h, penalty.BlockUntil.UTC().Format(time.RFC3339), penalty.EscalationLevel)
	} else {
		log.Printf("rider_abuse: state_update rider_user_id=%d count24h=%d block_until=none level=%d", riderUserID, count24h, penalty.EscalationLevel)
	}

	return penalty, nil
}

// CheckRiderBlock returns current block status for a rider. If not blocked, it returns nil.
func CheckRiderBlock(ctx context.Context, db *sql.DB, riderUserID int64, now time.Time) (*RiderPenaltyState, error) {
	if riderUserID == 0 {
		return nil, nil
	}
	state, err := loadState(ctx, db, riderUserID)
	if err != nil || state == nil || !state.BlockUntil.Valid {
		return nil, err
	}
	t, err := parseTime(state.BlockUntil.String)
	if err != nil {
		return nil, nil
	}
	nowUTC := now.UTC()
	if !t.After(nowUTC) {
		// Block expired; keep state but do not treat as blocked.
		return nil, nil
	}
	return &RiderPenaltyState{
		BlockUntil:      &t,
		BlockDuration:   t.Sub(nowUTC),
		EscalationLevel: state.EscalationLevel,
	}, nil
}

// FormatRemaining returns a short human-friendly remaining duration string in Uzbek.
func FormatRemaining(until time.Time, now time.Time) string {
	dur := until.Sub(now)
	if dur <= 0 {
		return "hozir"
	}
	minutes := int(dur.Minutes() + 0.5)
	if minutes < 60 {
		return fmtDurationUz(minutes, 0)
	}
	hours := minutes / 60
	mins := minutes % 60
	return fmtDurationUz(hours, mins)
}

func fmtDurationUz(hours, minutes int) string {
	if hours > 0 && minutes > 0 {
		return fmt.Sprintf("%d soat %d daqiqadan so‘ng", hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%d soatdan so‘ng", hours)
	}
	return fmt.Sprintf("%d daqiqadan so‘ng", minutes)
}

// --- internal helpers ---

type stateRow struct {
	RiderUserID    int64
	BlockUntil     sql.NullString
	LastWarningAt  sql.NullString
	EscalationLevel int
}

func insertAbuseEvent(ctx context.Context, db *sql.DB, riderUserID int64, tripID string, now time.Time) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO rider_abuse_events (rider_user_id, trip_id, created_at)
		VALUES (?1, ?2, ?3)`,
		riderUserID, tripID, now.UTC().Format("2006-01-02 15:04:05"))
	if err == nil {
		log.Printf("rider_abuse: event_recorded rider_user_id=%d trip_id=%s", riderUserID, tripID)
	}
	return err
}

func countEvents24h(ctx context.Context, db *sql.DB, riderUserID int64, now time.Time) (int, error) {
	var count int
	from := now.Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM rider_abuse_events
		WHERE rider_user_id = ?1 AND created_at >= ?2`,
		riderUserID, from).Scan(&count)
	return count, err
}

func loadState(ctx context.Context, db *sql.DB, riderUserID int64) (*stateRow, error) {
	var row stateRow
	err := db.QueryRowContext(ctx, `
		SELECT rider_user_id, block_until, last_warning_at, escalation_level
		FROM rider_abuse_state WHERE rider_user_id = ?1`,
		riderUserID).Scan(&row.RiderUserID, &row.BlockUntil, &row.LastWarningAt, &row.EscalationLevel)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func upsertState(ctx context.Context, db *sql.DB, s *stateRow) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO rider_abuse_state (rider_user_id, block_until, last_warning_at, escalation_level)
		VALUES (?1, ?2, ?3, ?4)
		ON CONFLICT (rider_user_id) DO UPDATE SET
			block_until = excluded.block_until,
			last_warning_at = excluded.last_warning_at,
			escalation_level = excluded.escalation_level`,
		s.RiderUserID, nullStringOrNil(s.BlockUntil), nullStringOrNil(s.LastWarningAt), s.EscalationLevel)
	return err
}

func nullStringOrNil(ns sql.NullString) interface{} {
	if ns.Valid {
		return ns.String
	}
	return nil
}

func parseTime(s string) (time.Time, error) {
	// Same layout used in other parts of the codebase.
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

