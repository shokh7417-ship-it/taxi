package logger

import (
	"context"
	"log/slog"
	"os"
)

var Log *slog.Logger

func init() {
	Log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// TripEvent logs a structured trip-related event (start, finish, cancel, etc.).
func TripEvent(action string, tripID string, result string, attrs ...slog.Attr) {
	a := make([]slog.Attr, 0, 4+len(attrs))
	a = append(a,
		slog.String("action", action),
		slog.String("trip_id", tripID),
		slog.String("result", result),
	)
	a = append(a, attrs...)
	Log.LogAttrs(context.Background(), slog.LevelInfo, "trip_event", a...)
}

// TripEventAttrs returns common attrs for trip events. Pass 0 for unused IDs.
func TripEventAttrs(driverUserID, riderUserID int64) []slog.Attr {
	var a []slog.Attr
	if driverUserID != 0 {
		a = append(a, slog.Int64("driver_user_id", driverUserID))
	}
	if riderUserID != 0 {
		a = append(a, slog.Int64("rider_user_id", riderUserID))
	}
	return a
}

// DriverLocation logs driver location accepted or ignored with reason.
func DriverLocation(tripID string, driverUserID int64, result string, reason string) {
	attrs := []slog.Attr{
		slog.String("trip_id", tripID),
		slog.Int64("driver_user_id", driverUserID),
		slog.String("result", result),
	}
	if reason != "" {
		attrs = append(attrs, slog.String("ignore_reason", reason))
	}
	Log.LogAttrs(context.Background(), slog.LevelInfo, "driver_location", attrs...)
}

// WebSocketEvent logs websocket connect or disconnect.
func WebSocketEvent(event string, tripID string, userID int64, attrs ...slog.Attr) {
	a := []slog.Attr{
		slog.String("event", event),
		slog.String("trip_id", tripID),
		slog.Int64("user_id", userID),
	}
	a = append(a, attrs...)
	Log.LogAttrs(context.Background(), slog.LevelInfo, "websocket", a...)
}

// AuthFailure logs auth failure (missing/invalid init data, not authorized).
func AuthFailure(reason string, attrs ...slog.Attr) {
	a := []slog.Attr{slog.String("reason", reason)}
	a = append(a, attrs...)
	Log.LogAttrs(context.Background(), slog.LevelWarn, "auth_failure", a...)
}
