package web

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"tshirt2/internal/db"
)

// Flask 版のコードで作った DB (Werkzeug のハッシュ入り) を、Go 版のサーバーがそのまま使えること。
// 同じ inventory.db を、Flask 版から Go 版へ切り替える運用の、端から端までの確認。
func TestGoServerWorksOnAFlaskMadeDatabase(t *testing.T) {
	src, err := os.Open("../service/testdata/golden/inventory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "inventory.db")
	dst, _ := os.Create(path)
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	h := newHarnessAt(t, path, d)
	admin := h.client()
	if r := admin.login("admin", testPassword, true); r.status != 302 || r.location() != "/" {
		t.Fatalf("login with a Flask-made scrypt hash: %d %q", r.status, r.location())
	}
	if r := admin.get("/admin/users"); r.status != 200 {
		t.Errorf("admin page: %d", r.status)
	}
	staff := h.client()
	staff.mustLogin("staff", false)
	if r := staff.get("/admin/users"); r.status != 403 {
		t.Errorf("staff on admin page: %d, want 403", r.status)
	}
	if r := h.client().login("admin", "wrong", false); r.status != 200 {
		t.Errorf("wrong password: %d", r.status)
	}
}
