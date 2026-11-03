package auth

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Werkzeug が実際に作ったハッシュ (tools/gen_hash_vectors.py) を、Go が検証できること。
func TestVerifyWerkzeugHashes(t *testing.T) {
	b, err := os.ReadFile("testdata/werkzeug_hashes.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Pw, Hash string }
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 11 {
		t.Fatalf("too few vectors: %d", len(cases))
	}
	kinds := map[string]bool{}
	for _, c := range cases {
		kinds[strings.SplitN(c.Hash, ":", 2)[0]] = true
		if ok, err := Verify(c.Hash, c.Pw); err != nil || !ok {
			t.Errorf("verify failed for %.24s…: ok=%v err=%v", c.Hash, ok, err)
		}
		if ok, _ := Verify(c.Hash, c.Pw+"x"); ok {
			t.Errorf("wrong password accepted for %.24s…", c.Hash)
		}
	}
	if !kinds["scrypt"] || !kinds["pbkdf2"] {
		t.Errorf("vectors should cover scrypt and pbkdf2: %v", kinds)
	}
}

func TestHashFormatAndRoundTrip(t *testing.T) {
	h, err := Hash("テストpass123")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^scrypt:32768:8:1\$[A-Za-z0-9]{16}\$[0-9a-f]{128}$`).MatchString(h) {
		t.Fatalf("unexpected format: %q", h)
	}
	if ok, err := Verify(h, "テストpass123"); err != nil || !ok {
		t.Errorf("round trip failed: %v %v", ok, err)
	}
	if ok, _ := Verify(h, "テストpass124"); ok {
		t.Error("wrong password accepted")
	}
	h2, _ := Hash("テストpass123")
	if h == h2 {
		t.Error("two hashes of the same password are identical (salt not random)")
	}
}

// Go が作ったハッシュを Werkzeug の check_password_hash で確かめるための出力 (tools/crosscheck_hash.py)。
func TestWriteHashesForWerkzeugCrosscheck(t *testing.T) {
	out := os.Getenv("GO_HASH_OUT")
	if out == "" {
		t.Skip("GO_HASH_OUT not set")
	}
	var cases []map[string]string
	for _, pw := range []string{"password123", "テストpass123", strings.Repeat("a", 40), "p@ss word\t!"} {
		h, err := Hash(pw)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, map[string]string{"pw": pw, "hash": h})
	}
	b, _ := json.Marshal(cases)
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsMalformedHashesWithoutPanicking(t *testing.T) {
	bad := []string{
		"", "plain", "a$b", "a$b$c$d", "scrypt:32768:8:1$salt$zz", "scrypt:32768:8:1$salt$",
		"scrypt:abc:8:1$salt$00", "scrypt:32768:8$salt$00", "scrypt:0:8:1$salt$00", "scrypt:32768:0:1$salt$00",
		"pbkdf2:sha256$salt$00", "pbkdf2:md5:1000$salt$00", "pbkdf2:sha256:0$salt$00", "pbkdf2:sha256:x$salt$00",
		"unknown:1:2:3$salt$00",
		"scrypt:1073741824:8:1$salt$00", // 巨大なメモリを要求する値
		"pbkdf2:sha256:999999999999$salt$00",
	}
	for _, h := range bad {
		ok, err := Verify(h, "x")
		if ok || err == nil {
			t.Errorf("Verify(%q) = %v, %v; want false + error", h, ok, err)
		}
	}
	if _, err := Verify("x", "y"); !errors.Is(err, ErrUnrecognizedHash) {
		t.Errorf("err = %v, want ErrUnrecognizedHash", err)
	}
}

func TestLoadSecretKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INVENTORY_SECRET_KEY", "")
	k1, err := LoadSecretKey(dir + "/instance")
	if err != nil || len(k1) != 64 {
		t.Fatalf("generate: %q, %v", k1, err)
	}
	k2, err := LoadSecretKey(dir + "/instance")
	if err != nil || string(k1) != string(k2) {
		t.Errorf("key not persisted: %q vs %q (%v)", k1, k2, err)
	}
	if st, err := os.Stat(dir + "/instance/secret_key"); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("secret_key mode = %v, %v; want 0600", st.Mode(), err)
	}
	t.Setenv("INVENTORY_SECRET_KEY", "from-env")
	if k, _ := LoadSecretKey(dir + "/instance"); string(k) != "from-env" {
		t.Errorf("env not preferred: %q", k)
	}
}
