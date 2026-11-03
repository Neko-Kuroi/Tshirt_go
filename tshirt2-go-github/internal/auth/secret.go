package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LoadSecretKey は Cookie の署名に使う鍵を返す (Flask 版 config._load_secret_key と同じ順)。
// 環境変数 INVENTORY_SECRET_KEY → <instanceDir>/secret_key。無ければ生成して保存する。
func LoadSecretKey(instanceDir string) ([]byte, error) {
	if env := os.Getenv("INVENTORY_SECRET_KEY"); env != "" {
		return []byte(env), nil
	}
	path := filepath.Join(instanceDir, "secret_key")
	b, err := os.ReadFile(path)
	if err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return []byte(s), nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(raw)
	// 他のプロセスが同時に作った場合に上書きしないよう、新規作成のときだけ書く
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return LoadSecretKey(instanceDir)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(key); err != nil {
		return nil, err
	}
	return []byte(key), nil
}
