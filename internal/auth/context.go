package auth

import "context"

type contextKey string

const authContextKey contextKey = "auth_user"

// User is the authenticated user attached to the request context.
type User struct {
	UserID         int64  // internal users.id
	TelegramUserID int64  // Telegram user id from initData
	Role           string // "driver" or "rider"
}

// WithUser returns a context with the given User attached.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, authContextKey, u)
}

// UserFromContext returns the authenticated User from context, or nil if not set.
func UserFromContext(ctx context.Context) *User {
	v := ctx.Value(authContextKey)
	if v == nil {
		return nil
	}
	u, _ := v.(*User)
	return u
}
