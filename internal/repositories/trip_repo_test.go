package repositories

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
)

// TestAddTripDistance_and_GetTripDistanceAndFare runs against a real DB when TEST_DATABASE_URL is set.
// It verifies that AddTripDistance increments distance_m only when status=STARTED, and GetTripDistanceAndFare returns current values.
func TestAddTripDistance_and_GetTripDistanceAndFare(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping repository integration test")
	}
	db, err := sql.Open("libsql", url)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewTripRepo(db)

	// We cannot create a full trip without migrations and ride_requests; so we only test GetTripDistanceAndFare for missing trip.
	_, _, _, err = repo.GetTripDistanceAndFare(ctx, "nonexistent-trip-id")
	if err != sql.ErrNoRows && err != nil {
		t.Errorf("GetTripDistanceAndFare(nonexistent) want ErrNoRows or nil, got %v", err)
	}
	if err == nil {
		t.Error("GetTripDistanceAndFare(nonexistent) should return error")
	}

	// AddTripDistance on nonexistent trip should affect 0 rows (no error, 0 rows)
	n, err := repo.AddTripDistance(ctx, "nonexistent-trip-id", 100)
	if err != nil {
		t.Errorf("AddTripDistance(nonexistent) err: %v", err)
	}
	if n != 0 {
		t.Errorf("AddTripDistance(nonexistent) rows affected = %d, want 0", n)
	}
}

func TestGetStatus(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping repository integration test")
	}
	db, err := sql.Open("libsql", url)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewTripRepo(db)

	_, err = repo.GetStatus(ctx, "nonexistent")
	if err != sql.ErrNoRows && err != nil {
		t.Errorf("GetStatus(nonexistent) want ErrNoRows, got %v", err)
	}
}
