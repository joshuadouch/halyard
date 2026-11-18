package auth

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSHA512Crypt(t *testing.T) {
	// Vector from the SHA-crypt specification.
	if got, want := sha512Crypt("Hello world!", "saltstring"), "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	long := strings.Repeat("correct horse battery staple ", 5)
	for _, pw := range []string{"", "a", long} {
		h := HashCrypt(pw)
		if !strings.HasPrefix(h, "$6$") || len(h) != 3+16+1+86 {
			t.Fatalf("bad hash %q", h)
		}
		if _, err := exec.LookPath("openssl"); err == nil && pw != "" {
			salt := strings.Split(h, "$")[2]
			out, err := exec.Command("openssl", "passwd", "-6", "-salt", salt, pw).Output()
			if err == nil && strings.TrimSpace(string(out)) != h {
				t.Fatalf("openssl disagrees for %q: %s vs %s", pw, out, h)
			}
		}
	}
}
