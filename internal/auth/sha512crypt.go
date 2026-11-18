package auth

import (
	"crypto/rand"
	"crypto/sha512"
	"strings"
)

const crypt64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// HashCrypt returns a SHA-512 crypt(3) hash ("$6$salt$..."), the format
// HAProxy userlists check with the system crypt library.
func HashCrypt(password string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	salt := make([]byte, 16)
	for i, x := range b {
		salt[i] = crypt64[int(x)%64]
	}
	return sha512Crypt(password, string(salt))
}

// sha512Crypt implements Ulrich Drepper's SHA-crypt with the default 5000
// rounds. salt is at most 16 characters.
func sha512Crypt(password, salt string) string {
	if len(salt) > 16 {
		salt = salt[:16]
	}
	p, s := []byte(password), []byte(salt)
	const rounds = 5000

	h := sha512.New()
	h.Write(p)
	h.Write(s)
	h.Write(p)
	alt := h.Sum(nil)

	h = sha512.New()
	h.Write(p)
	h.Write(s)
	n := len(p)
	for ; n > 64; n -= 64 {
		h.Write(alt)
	}
	h.Write(alt[:n])
	for n = len(p); n > 0; n >>= 1 {
		if n&1 != 0 {
			h.Write(alt)
		} else {
			h.Write(p)
		}
	}
	a := h.Sum(nil)

	h = sha512.New()
	for range len(p) {
		h.Write(p)
	}
	dp := h.Sum(nil)
	pseq := repeat(dp, len(p))

	h = sha512.New()
	for range 16 + int(a[0]) {
		h.Write(s)
	}
	ds := h.Sum(nil)
	sseq := repeat(ds, len(s))

	c := a
	for i := range rounds {
		h = sha512.New()
		if i&1 != 0 {
			h.Write(pseq)
		} else {
			h.Write(c)
		}
		if i%3 != 0 {
			h.Write(sseq)
		}
		if i%7 != 0 {
			h.Write(pseq)
		}
		if i&1 != 0 {
			h.Write(c)
		} else {
			h.Write(pseq)
		}
		c = h.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$6$")
	out.WriteString(salt)
	out.WriteByte('$')
	enc := func(b2, b1, b0 byte, n int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for range n {
			out.WriteByte(crypt64[w&0x3f])
			w >>= 6
		}
	}
	order := [][3]int{{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13}, {56, 14, 35},
		{15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41}}
	for _, o := range order {
		enc(c[o[0]], c[o[1]], c[o[2]], 4)
	}
	enc(0, 0, c[63], 2)
	return out.String()
}

func repeat(d []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out)+len(d) <= n {
		out = append(out, d...)
	}
	return append(out, d[:n-len(out)]...)
}
