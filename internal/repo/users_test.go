package repo

import "testing"

// Python (hmac.new(key, password_hash, sha256).hexdigest()[:20]) と同じ値になること。
func TestSessionTokenMatchesPython(t *testing.T) {
	cases := []struct {
		secret, hash, want string
	}{
		{"test-secret", "scrypt:32768:8:1$AbCdEfGhIjKlMnOp$00ff", "c7c569a5fb9770027996"},
		{"日本語の鍵", "pbkdf2:sha256:1000000$salt$beef", "bb5a9fa1d457cc9983c6"},
	}
	for _, c := range cases {
		u := User{PasswordHash: c.hash}
		if got := u.SessionToken([]byte(c.secret)); got != c.want {
			t.Errorf("SessionToken = %q, want %q", got, c.want)
		}
	}
	a := User{PasswordHash: "x"}.SessionToken([]byte("k"))
	b := User{PasswordHash: "y"}.SessionToken([]byte("k"))
	if a == b {
		t.Error("token must change when the password hash changes")
	}
}
