// Package web は HTTP の層 (Flask 版の app/__init__.py、auth、共通のテンプレートに相当)。
//
// この手順 (3) で作るのは、画面に依存しない共通部品:
// 署名付き Cookie のセッション、ログイン保持 (remember)、CSRF、flash、権限チェック、
// エラーページ、no-store、DB が未作成/古いときの案内 (503)、静的ファイル。
// 各画面 (手順4) は、Page / AdminPage / Public でルートを登録して、この上に載せる。
package web

import (
	"log"
	"time"
)

// Config は Server の設定。
type Config struct {
	DatabasePath string // inventory.db のパス
	SecretKey    []byte // Cookie の署名鍵 (auth.LoadSecretKey)

	// CookieSecure は Cookie に Secure 属性を付ける (HTTPS で公開するとき。INVENTORY_COOKIE_SECURE=1 相当)。
	CookieSecure bool

	SessionLifetime  time.Duration // セッションの有効期間。既定は14日 (Flask 版 PERMANENT_SESSION_LIFETIME)
	RememberDuration time.Duration // 「ログインを保持する」の有効期間。既定は365日 (Flask-Login の既定)
	CSRFTTL          time.Duration // 画面を開いてから送信できる時間。既定は1時間 (Flask-WTF の既定)

	// InitHint は、DB が未作成/古いときの 503 の案内に出す、管理者向けの手順。
	InitHint string

	Logger *log.Logger
	Now    func() time.Time // テスト用。nil なら time.Now
}

func (c *Config) applyDefaults() {
	if c.SessionLifetime == 0 {
		c.SessionLifetime = 14 * 24 * time.Hour
	}
	if c.RememberDuration == 0 {
		c.RememberDuration = 365 * 24 * time.Hour
	}
	if c.CSRFTTL == 0 {
		c.CSRFTTL = time.Hour
	}
	if c.InitHint == "" {
		c.InitHint = "サーバーで init-db を実行すると、再起動なしで使えるようになります"
	}
	if c.Logger == nil {
		c.Logger = log.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}
