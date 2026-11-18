package auth

import (
	"testing"
	"time"
)

func TestTOTPRFC6238(t *testing.T) {
	// RFC 6238 appendix B, SHA1 seed "12345678901234567890" (last 6 digits).
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range cases {
		got, err := totpAt(secret, time.Unix(ts, 0))
		if err != nil || got != want {
			t.Errorf("t=%d: got %s (%v), want %s", ts, got, err, want)
		}
	}
}

func TestCheckTOTP(t *testing.T) {
	s := NewTOTPSecret()
	if !CheckTOTP(s, TOTPCode(s)) {
		t.Fatal("current code rejected")
	}
	if CheckTOTP(s, "12345") || CheckTOTP(s, "") {
		t.Fatal("malformed code accepted")
	}
}

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong password!") {
		t.Fatal("password check is wrong")
	}
}

func TestTeamDomain(t *testing.T) {
	for in, want := range map[string]string{
		"acme":                               "acme.cloudflareaccess.com",
		"acme.cloudflareaccess.com":          "acme.cloudflareaccess.com",
		"https://acme.cloudflareaccess.com/": "acme.cloudflareaccess.com",
	} {
		got, err := TeamDomain(in)
		if err != nil || got != want {
			t.Errorf("TeamDomain(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := TeamDomain("evil.com/x"); err == nil {
		t.Error("accepted a non-Access domain")
	}
}
