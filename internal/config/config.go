package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds application configuration loaded from environment.
type Config struct {
	RiderBotToken           string
	DriverBotToken          string
	DatabaseURL             string
	StartingFee             int     // Base fare when trip starts (so'm)
	PricePerKm              int     // Per-km rate (so'm)
	MatchRadiusKm           float64
	ExpandedRadiusKm        float64   // Radius after expansion if no driver (e.g. 4)
	RadiusExpansionMinutes  int       // Minutes before expanding radius
	RequestExpiresSeconds   int
	DriverSeenSeconds       int
	StartReminderSeconds    int
	WebAppURL               string   // Base URL for Telegram Mini App / driver map (e.g. https://example.com/webapp)
	RiderMapURL             string   // Full URL to rider map HTML (e.g. https://example.com/webapp/rider-map.html); if empty, derived as WebAppURL + "/rider-map.html"
	APIAddr                 string   // HTTP API address for driver location and trip (e.g. :8080)
	EnableDriverIDHeader    bool     // If true, allow X-Driver-Id header as fallback when init data is missing (only if you trust the Mini App URL)
	AdminID                 int64    // Telegram user ID of the admin (only this user can use admin bot fare menu)
	AdminBotToken           string   // Telegram bot token for admin bot (optional; if empty, admin bot is not started)
	InfiniteDriverBalance   bool     // If true, dispatch ignores balance and no commission is deducted (temporary launch mode)
	CommissionPercent       int      // Commission percentage on fare when InfiniteDriverBalance is false (e.g. 5 or 10)
	DispatchDebug           bool     // If true, emit verbose dispatch/grid debug logs
	// Dispatch tuning: priority queue (one driver at a time, then next after timeout)
	DispatchWaitSeconds         int // Seconds to wait for a driver batch to accept before trying next (e.g. 60)
	DispatchDriverCooldownSec   int // Cooldown before sending another request to the same driver (e.g. 5–10)
	// PickupStartMaxMeters: driver must be within this distance of pickup to start from WAITING (or to mark ARRIVED).
	PickupStartMaxMeters int
}

// Load reads .env (if present) and builds Config from env with defaults.
func Load() (*Config, error) {
	_ = godotenv.Load()

	startingFee, _ := strconv.Atoi(getEnv("STARTING_FEE", "4000"))
	pricePerKm, _ := strconv.Atoi(getEnv("PRICE_PER_KM", "1500"))
	matchRadiusKm, _ := strconv.ParseFloat(getEnv("MATCH_RADIUS_KM", "3"), 64)
	expandedRadiusKm, _ := strconv.ParseFloat(getEnv("EXPANDED_RADIUS_KM", "4"), 64)
	radiusExpansionMin, _ := strconv.Atoi(getEnv("RADIUS_EXPANSION_MINUTES", "5"))
	requestExpires, _ := strconv.Atoi(getEnv("REQUEST_EXPIRES_SECONDS", "120")) // 2 min TTL: request no longer sent after this
	driverSeen, _ := strconv.Atoi(getEnv("DRIVER_SEEN_SECONDS", "600")) // 10 min: orders pushed to drivers seen in last 10 min
	startReminder, _ := strconv.Atoi(getEnv("START_REMINDER_SECONDS", "60"))
	pickupStartMaxM, _ := strconv.Atoi(getEnv("PICKUP_START_MAX_METERS", "100"))

	cfg := &Config{
		RiderBotToken:          getEnv("RIDER_BOT_TOKEN", ""),
		DriverBotToken:         getEnv("DRIVER_BOT_TOKEN", ""),
		DatabaseURL:            getDatabaseURL(),
		StartingFee:            startingFee,
		PricePerKm:             pricePerKm,
		MatchRadiusKm:          matchRadiusKm,
		ExpandedRadiusKm:       expandedRadiusKm,
		RadiusExpansionMinutes: radiusExpansionMin,
		RequestExpiresSeconds:  requestExpires,
		DriverSeenSeconds:      driverSeen,
		StartReminderSeconds:   startReminder,
		WebAppURL:              getEnv("WEBAPP_URL", "https://example.com/webapp"),
		RiderMapURL:            getRiderMapURL(getEnv("WEBAPP_URL", "https://example.com/webapp"), getEnv("RIDER_MAP_URL", "")),
		APIAddr:                getAPIAddr(),
		EnableDriverIDHeader:   getEnv("ENABLE_DRIVER_ID_HEADER", "") == "true" || getEnv("ENABLE_DRIVER_ID_HEADER", "") == "1",
		AdminID:                getEnvInt64("ADMIN_ID", 0),
		AdminBotToken:          getEnvFirst("ADMIN_BOT_TOKEN", "ADMIN_BOT", ""),
		InfiniteDriverBalance:  getEnv("INFINITE_DRIVER_BALANCE", "true") == "true" || getEnv("INFINITE_DRIVER_BALANCE", "true") == "1",
		CommissionPercent:      getEnvInt("COMMISSION_PERCENT", 5),
		DispatchDebug:            getEnv("DISPATCH_DEBUG", "") == "true" || getEnv("DISPATCH_DEBUG", "") == "1",
		DispatchWaitSeconds:       getEnvInt("DISPATCH_WAIT_SECONDS", 60),
		DispatchDriverCooldownSec: getEnvInt("DISPATCH_DRIVER_COOLDOWN_SECONDS", 5),
		PickupStartMaxMeters:      pickupStartMaxM,
	}

	if cfg.RiderBotToken == "" {
		return nil, fmt.Errorf("RIDER_BOT_TOKEN is required")
	}
	if cfg.DriverBotToken == "" {
		return nil, fmt.Errorf("DRIVER_BOT_TOKEN is required")
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL or TURSO_DATABASE_URL + TURSO_AUTH_TOKEN required")
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// getEnvFirst returns the first non-empty env value for the given keys; last argument is the default.
// Example: getEnvFirst("ADMIN_BOT_TOKEN", "ADMIN_BOT", "") tries both vars, then returns "".
func getEnvFirst(keys ...string) string {
	if len(keys) < 2 {
		return ""
	}
	defaultVal := keys[len(keys)-1]
	for i := 0; i < len(keys)-1; i++ {
		if v := os.Getenv(keys[i]); v != "" {
			return v
		}
	}
	return defaultVal
}

func getEnvInt64(key string, defaultVal int64) int64 {
	s := os.Getenv(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return defaultVal
	}
	return v
}

func getEnvInt(key string, defaultVal int) int {
	s := os.Getenv(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

// getRiderMapURL returns RIDER_MAP_URL if set, otherwise derives from webAppURL: same base path + "/rider-map.html".
// Example: webAppURL "https://your-domain.com/webapp" -> "https://your-domain.com/webapp/rider-map.html".
func getRiderMapURL(webAppURL, riderMapURL string) string {
	if riderMapURL != "" {
		return strings.TrimSuffix(riderMapURL, "/")
	}
	base := strings.TrimSuffix(webAppURL, "/")
	if base == "" {
		return ""
	}
	return base + "/rider-map.html"
}

// getAPIAddr returns the HTTP listen address. Uses PORT (e.g. from Railway/Render) if set, else API_ADDR.
func getAPIAddr() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return getEnv("API_ADDR", ":8080")
}

// getDatabaseURL returns the Turso libSQL connection URL.
// Use DATABASE_URL (full libsql://...?authToken=...) or TURSO_DATABASE_URL + TURSO_AUTH_TOKEN.
func getDatabaseURL() string {
	if u := os.Getenv("DATABASE_URL"); u != "" {
		return u
	}
	url := os.Getenv("TURSO_DATABASE_URL")
	token := os.Getenv("TURSO_AUTH_TOKEN")
	if url != "" && token != "" {
		sep := "?"
		if len(url) > 0 && url[len(url)-1] == '?' {
			sep = ""
		} else if strings.Contains(url, "?") {
			sep = "&"
		}
		return url + sep + "authToken=" + token
	}
	return ""
}
