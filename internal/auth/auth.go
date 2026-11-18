// Package auth handles local passwords, TOTP and Cloudflare Access JWTs.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/crypto/bcrypt"
)

// HashPassword hashes a password with bcrypt.
func HashPassword(pw string) (string, error) {
	if len(pw) < 10 {
		return "", errors.New("password must be at least 10 characters")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

// CheckPassword compares a password with its hash.
func CheckPassword(hash, pw string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// RandomToken returns a URL-safe random token.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewTOTPSecret returns a base32 TOTP secret.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// TOTPURI returns an otpauth:// URI for authenticator apps.
func TOTPURI(secret, account string) string {
	return fmt.Sprintf("otpauth://totp/Halyard:%s?secret=%s&issuer=Halyard&algorithm=SHA1&digits=6&period=30",
		url.PathEscape(account), secret)
}

func totpAt(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code), nil
}

// CheckTOTP accepts the current code and one step either side.
func CheckTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	now := time.Now()
	for _, d := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		if c, err := totpAt(secret, now.Add(d)); err == nil && hmac.Equal([]byte(c), []byte(code)) {
			return true
		}
	}
	return false
}

// TOTPCode returns the current code (used by tests).
func TOTPCode(secret string) string {
	c, _ := totpAt(secret, time.Now())
	return c
}

// AccessVerifier validates Cloudflare Access JWTs (Cf-Access-Jwt-Assertion).
type AccessVerifier struct {
	HTTP *http.Client
	// CertsURL overrides the JWKS location (tests).
	CertsURL func(team string) string

	mu   sync.Mutex
	keys map[string]*cachedKeys
}

type cachedKeys struct {
	set     jose.JSONWebKeySet
	fetched time.Time
}

var teamRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// TeamDomain normalises "example" or "example.cloudflareaccess.com" to the
// full team domain.
func TeamDomain(team string) (string, error) {
	team = strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(team)), "https://"), "/")
	team = strings.TrimSuffix(team, ".cloudflareaccess.com")
	if !teamRe.MatchString(team) {
		return "", errors.New("invalid Access team name")
	}
	return team + ".cloudflareaccess.com", nil
}

func (v *AccessVerifier) keySet(ctx context.Context, team string, force bool) (jose.JSONWebKeySet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys == nil {
		v.keys = map[string]*cachedKeys{}
	}
	if c, ok := v.keys[team]; ok && !force && time.Since(c.fetched) < time.Hour {
		return c.set, nil
	}
	u := "https://" + team + "/cdn-cgi/access/certs"
	if v.CertsURL != nil {
		u = v.CertsURL(team)
	}
	hc := v.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := hc.Do(req)
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return jose.JSONWebKeySet{}, fmt.Errorf("access certs: HTTP %d", resp.StatusCode)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return set, err
	}
	v.keys[team] = &cachedKeys{set: set, fetched: time.Now()}
	return set, nil
}

// Verify checks the token and returns the authenticated email.
func (v *AccessVerifier) Verify(ctx context.Context, team, aud, token string) (string, error) {
	domain, err := TeamDomain(team)
	if err != nil {
		return "", err
	}
	if aud == "" {
		return "", errors.New("access AUD tag not configured")
	}
	tok, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return "", err
	}
	var claims jwt.Claims
	var extra struct {
		Email string `json:"email"`
	}
	verify := func(force bool) error {
		set, err := v.keySet(ctx, domain, force)
		if err != nil {
			return err
		}
		return tok.Claims(set, &claims, &extra)
	}
	if err := verify(false); err != nil {
		// Keys rotate; refetch once.
		if err2 := verify(true); err2 != nil {
			return "", fmt.Errorf("access token signature: %w", err2)
		}
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{
		Issuer:      "https://" + domain,
		AnyAudience: jwt.Audience{aud},
		Time:        time.Now(),
	}, time.Minute); err != nil {
		return "", err
	}
	if extra.Email == "" {
		return "", errors.New("access token has no email (service tokens are not supported)")
	}
	return extra.Email, nil
}
