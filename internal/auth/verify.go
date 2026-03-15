package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// InitDataUser is the "user" object inside Telegram Mini App initData (JSON).
type InitDataUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	PhotoURL  string `json:"photo_url"`
}

// VerifyMiniAppInitData verifies initData from Telegram.WebApp.initData using the bot token.
// Returns the Telegram user ID and nil error if valid. Does not trust any field until hash is verified.
func VerifyMiniAppInitData(botToken, initData string) (telegramUserID int64, err error) {
	if botToken == "" || initData == "" {
		return 0, errors.New("auth: missing bot token or init data")
	}
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return 0, err
	}
	hashReceived := vals.Get("hash")
	if hashReceived == "" {
		return 0, errors.New("auth: hash missing in init data")
	}
	vals.Del("hash")
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(vals.Get(k))
	}
	dataCheckString := b.String()
	secretKey := hmacSHA256([]byte(botToken), []byte("WebAppData"))
	computedHash := hmacSHA256([]byte(dataCheckString), secretKey)
	computedHex := hex.EncodeToString(computedHash)
	if !hmac.Equal([]byte(computedHex), []byte(hashReceived)) {
		return 0, errors.New("auth: init data hash mismatch")
	}
	userJSON := vals.Get("user")
	if userJSON == "" {
		return 0, errors.New("auth: user missing in init data")
	}
	var u InitDataUser
	if err := json.Unmarshal([]byte(userJSON), &u); err != nil {
		return 0, err
	}
	return u.ID, nil
}

func hmacSHA256(message, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

// ParseAuthDate returns auth_date from initData (Unix timestamp). Optional for expiry check.
func ParseAuthDate(initData string) (int64, error) {
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return 0, err
	}
	s := vals.Get("auth_date")
	if s == "" {
		return 0, errors.New("auth: auth_date missing")
	}
	return strconv.ParseInt(s, 10, 64)
}
