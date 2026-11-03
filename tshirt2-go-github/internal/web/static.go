package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// staticHandler は埋め込んだ静的ファイルを配る。内容のハッシュを ETag にして、
// 毎回確認 (no-cache) させつつ、変わっていなければ 304 を返す (Flask の static と同じ振る舞い)。
func staticHandler(notFound http.HandlerFunc) http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	etags := map[string]string{}
	fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(sub, p)
			sum := sha256.Sum256(b)
			etags[p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		}
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/static/")), "/")
		etag, ok := etags[name]
		if !ok {
			notFound(w, r)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if m := r.Header.Get("If-None-Match"); m == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		b, _ := fs.ReadFile(sub, name)
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Write(b)
	})
}
