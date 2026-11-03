package repo

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"

	"tshirt2/internal/db"
)

// User は利用者 (Flask 版 models.User)。
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	IsActive     bool
}

func (u User) IsAdmin() bool { return u.Role == "admin" }

// SessionToken はパスワードを変えると変わる値 (Flask 版 session_token と同じ式)。
// ハッシュそのものは Cookie に出さず、秘密鍵での HMAC にする。
func (u User) SessionToken(secret []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(u.PasswordHash))
	return hex.EncodeToString(m.Sum(nil))[:20]
}

const userSQL = `SELECT id, username, password_hash, role, is_active FROM "user"`

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var active int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &active)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.IsActive = active != 0
	return &u, nil
}

// GetUser は id でユーザーを返す。無ければ nil。
func GetUser(ctx context.Context, q db.Querier, id int64) (*User, error) {
	return scanUser(q.QueryRowContext(ctx, userSQL+" WHERE id = ?", id))
}

// GetUserByName はユーザー名 (完全一致) でユーザーを返す。無ければ nil。
func GetUserByName(ctx context.Context, q db.Querier, username string) (*User, error) {
	return scanUser(q.QueryRowContext(ctx, userSQL+" WHERE username = ?", username))
}
