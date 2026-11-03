// Package auth はパスワードの検証・生成と、署名鍵の読み込み。
//
// 保存形式は Flask 版 (Werkzeug の generate_password_hash) と同じなので、同じ inventory.db を
// 両方の版で使える。検証は scrypt と pbkdf2 の両方 (古い DB は pbkdf2 のことがある)。
// 新しいパスワードは、Werkzeug 3 の既定と同じ scrypt:32768:8:1 で保存する。
package auth

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"runtime/debug"
	"strconv"
	"strings"

	"tshirt2/internal/xcrypto/pbkdf2"
	"tshirt2/internal/xcrypto/scrypt"
)

const (
	scryptN, scryptR, scryptP = 32768, 8, 1
	scryptKeyLen              = 64 // Werkzeug は既定の dklen=64 (hex で128文字)
	saltLen                   = 16
	saltChars                 = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// 保存済みのハッシュ文字列から読んだパラメーターの上限 (壊れた値で巨大なメモリを確保しないため)。
	maxScryptMem = 512 << 20
	maxPBKDF2Its = 50_000_000
)

// ErrUnrecognizedHash は、保存されたハッシュの形式が分からないとき。
var ErrUnrecognizedHash = errors.New("auth: unrecognized password hash format")

// deriveSem は、パスワード派生 (scrypt/pbkdf2) の同時実行数を絞る。
// scrypt は1回で約34MB の作業領域を使うため、無制限に並行すると、
// ログインが集中したときにメモリが同時実行数の分だけ跳ね上がる。
var deriveSem = make(chan struct{}, 4)

// deriveKey は鍵派生を1回実行し、終わったら作業領域を OS に返す。
// 既定の GC は割り当てが進んだときにしか走らないので、待機の多い
// サーバーでは、解放済みの作業領域 (数十MB) が回収されないまま
// RSS に残り続ける。ログインのような低頻度の処理なので、毎回
// FreeOSMemory で確実に返す。
func deriveKey(f func() ([]byte, error)) ([]byte, error) {
	deriveSem <- struct{}{}
	defer func() { <-deriveSem }()
	defer debug.FreeOSMemory()
	return f()
}

// Hash は新しいパスワードのハッシュを、Werkzeug 互換の scrypt 形式で作る。
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	for i := range salt {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(saltChars))))
		if err != nil {
			return "", err
		}
		salt[i] = saltChars[n.Int64()]
	}
	key, err := deriveKey(func() ([]byte, error) {
		return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt:%d:%d:%d$%s$%s", scryptN, scryptR, scryptP, salt, hex.EncodeToString(key)), nil
}

// Verify は、パスワードが保存済みのハッシュ ("scrypt:32768:8:1$salt$hex" や
// "pbkdf2:sha256:1000000$salt$hex") と一致するかを返す。形式が不明・壊れているときは error。
func Verify(stored, password string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 {
		return false, ErrUnrecognizedHash
	}
	method, salt, want := parts[0], parts[1], parts[2]
	wantBytes, err := hex.DecodeString(want)
	if err != nil || len(wantBytes) == 0 {
		return false, ErrUnrecognizedHash
	}
	m := strings.Split(method, ":")
	var got []byte
	switch m[0] {
	case "scrypt":
		if len(m) != 4 {
			return false, ErrUnrecognizedHash
		}
		n, e1 := strconv.Atoi(m[1])
		r, e2 := strconv.Atoi(m[2])
		p, e3 := strconv.Atoi(m[3])
		if e1 != nil || e2 != nil || e3 != nil || n < 2 || r < 1 || p < 1 {
			return false, ErrUnrecognizedHash
		}
		if int64(128)*int64(n)*int64(r) > maxScryptMem {
			return false, fmt.Errorf("auth: scrypt parameters too large (n=%d r=%d)", n, r)
		}
		// Werkzeug は、salt の文字列のバイト列をそのまま使う (base64 デコードしない)。
		// 派生鍵の長さは、保存済みのハッシュの長さから決める。
		got, err = deriveKey(func() ([]byte, error) {
			return scrypt.Key([]byte(password), []byte(salt), n, r, p, len(wantBytes))
		})
		if err != nil {
			return false, err
		}
	case "pbkdf2":
		if len(m) != 3 {
			return false, ErrUnrecognizedHash
		}
		var h func() hash.Hash
		switch m[1] {
		case "sha256":
			h = sha256.New
		case "sha512":
			h = sha512.New
		case "sha1":
			h = sha1.New
		default:
			return false, ErrUnrecognizedHash
		}
		its, e := strconv.Atoi(m[2])
		if e != nil || its <= 0 || its > maxPBKDF2Its {
			return false, ErrUnrecognizedHash
		}
		got, err = deriveKey(func() ([]byte, error) {
			return pbkdf2.Key([]byte(password), []byte(salt), its, len(wantBytes), h), nil
		})
		if err != nil {
			return false, err
		}
	default:
		return false, ErrUnrecognizedHash
	}
	return subtle.ConstantTimeCompare(got, wantBytes) == 1, nil
}
