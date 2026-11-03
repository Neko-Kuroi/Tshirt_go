package service

import (
	"context"
	"database/sql"
	"sync"
	"unicode/utf8"

	"tshirt2/internal/auth"
	"tshirt2/internal/db"
	"tshirt2/internal/repo"
	"tshirt2/internal/util"
)

// MinPasswordLen はパスワードの最小の長さ (文字数)。
const MinPasswordLen = 8

// 画面に出す文言 (Flask 版と同じ)。
const (
	MsgLoginFailed      = "ユーザー名またはパスワードが違います。"
	MsgLoginRequired    = "ログインしてください。"
	MsgOldPasswordWrong = "現在のパスワードが違います。"
	MsgPasswordTooShort = "新しいパスワードは8文字以上にしてください。"
	MsgConfirmMismatch  = "確認用パスワードが一致しません。"
	MsgPasswordChanged  = "パスワードを変更しました。"
)

var (
	dummyOnce sync.Once
	dummyHash string
)

// Authenticate はログイン画面の認証 (Flask 版 auth.login)。ユーザー名は前後の空白を除いて照合する。
// 失敗 (ユーザーなし・無効・パスワード違い) は、どれも nil を返す (理由を区別して漏らさない)。
// ユーザーが居ない場合も、ダミーのハッシュで照合して、応答時間からユーザー名の有無を推測されないようにする。
func Authenticate(ctx context.Context, q db.Querier, username, password string) (*repo.User, error) {
	u, err := repo.GetUserByName(ctx, q, util.PyStrip(username))
	if err != nil {
		return nil, err
	}
	hash := ""
	if u != nil {
		hash = u.PasswordHash
	} else {
		dummyOnce.Do(func() { dummyHash, _ = auth.Hash("dummy-password-for-timing") })
		hash = dummyHash
	}
	ok, verr := auth.Verify(hash, password)
	if u == nil || !u.IsActive || verr != nil || !ok { // 壊れたハッシュは、ログイン不可として扱う (500 にしない)
		return nil, nil
	}
	return u, nil
}

// ChangePassword は自分のパスワードの変更 (Flask 版 auth.change_password)。
// 入力の誤りは、画面に出す文言を返す (msg)。成功したら更新後のユーザーを返す。
func ChangePassword(ctx context.Context, d *db.DB, userID int64, oldPw, newPw, confirm string) (updated *repo.User, msg string, err error) {
	u, err := repo.GetUser(ctx, d, userID)
	if err != nil || u == nil {
		return nil, "", err
	}
	if ok, verr := auth.Verify(u.PasswordHash, oldPw); verr != nil || !ok {
		return nil, MsgOldPasswordWrong, nil
	}
	if utf8.RuneCountInString(newPw) < MinPasswordLen { // 文字数 (バイト数ではない)
		return nil, MsgPasswordTooShort, nil
	}
	if newPw != confirm {
		return nil, MsgConfirmMismatch, nil
	}
	hash, err := auth.Hash(newPw)
	if err != nil {
		return nil, "", err
	}
	err = d.Tx(ctx, func(c *sql.Conn) error {
		_, err := c.ExecContext(ctx, `UPDATE "user" SET password_hash = ? WHERE id = ?`, hash, userID)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	updated, err = repo.GetUser(ctx, d, userID)
	return updated, "", err
}
