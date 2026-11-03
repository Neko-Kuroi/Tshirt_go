package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// codec は、署名付きの値 (Cookie の中身) を作る・読む。値は「base64url(JSON).base64url(HMAC)」。
// 用途ごとに鍵を分ける (セッション用の値を、remember や flash として使い回せないように)。
type codec struct{ key []byte }

func newCodec(secret []byte, purpose string) codec {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("tshirt2/" + purpose))
	return codec{key: m.Sum(nil)}
}

var b64 = base64.RawURLEncoding

func (c codec) mac(payload string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

func (c codec) encode(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err) // 自分で作った構造体なので、失敗は実装の誤り
	}
	p := b64.EncodeToString(raw)
	return p + "." + b64.EncodeToString(c.mac(p))
}

// decode は署名を確かめて v に読み込む。改ざん・形式の誤りは false (エラーにしない)。
func (c codec) decode(s string, v any) bool {
	i := strings.IndexByte(s, '.')
	if i <= 0 || i == len(s)-1 {
		return false
	}
	p, sig := s[:i], s[i+1:]
	got, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(got, c.mac(p)) {
		return false
	}
	raw, err := b64.DecodeString(p)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}
