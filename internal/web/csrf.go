package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"strconv"
	"strings"
	"time"
)

// CSRF トークン: 「発行時刻.HMAC(鍵, セッションの乱数|発行時刻)」。
// セッションの乱数は署名付き Cookie の中にあるので、別のセッションで発行したトークンは通らない。
// 発行から CSRFTTL (既定1時間) を過ぎると無効 (Flask-WTF と同じ)。

func (s *Server) csrfMac(secret, ts string) string {
	m := hmac.New(sha256.New, s.csrfKey)
	m.Write([]byte(secret + "|" + ts))
	return b64.EncodeToString(m.Sum(nil))
}

func (s *Server) csrfMake(secret string, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	return ts + "." + s.csrfMac(secret, ts)
}

func (s *Server) csrfCheck(secret, token string, now time.Time) bool {
	i := strings.IndexByte(token, '.')
	if i <= 0 {
		return false
	}
	ts, mac := token[:i], token[i+1:]
	issued, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(issued, 0))
	if age > s.cfg.CSRFTTL || age < -time.Minute {
		return false
	}
	return hmac.Equal([]byte(mac), []byte(s.csrfMac(secret, ts)))
}
