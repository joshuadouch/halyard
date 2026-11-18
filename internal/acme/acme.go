// Package acme issues certificates with Let's Encrypt using DNS-01 through
// Cloudflare, so hosts that are only reachable on the LAN still get trusted
// certificates.
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
	"github.com/joshuadouch/halyard/internal/store"
)

type account struct {
	Email        string                 `json:"email"`
	KeyPEM       string                 `json:"key_pem"`
	Registration *registration.Resource `json:"registration"`
	Directory    string                 `json:"directory"`
	key          crypto.PrivateKey
}

func (a *account) GetEmail() string                        { return a.Email }
func (a *account) GetRegistration() *registration.Resource { return a.Registration }
func (a *account) GetPrivateKey() crypto.PrivateKey        { return a.key }

// Result is an issued certificate.
type Result struct {
	CertPEM  string
	KeyPEM   string
	NotAfter time.Time
	Issuer   string
}

// Issuer obtains certificates.
type Issuer struct {
	Store *store.Store
}

// Obtain issues a certificate for domains using the Cloudflare token.
func (i *Issuer) Obtain(ctx context.Context, email, cfToken string, staging bool, keyType string, domains []string) (*Result, error) {
	if cfToken == "" {
		return nil, errors.New("add a Cloudflare API token first")
	}
	if email == "" {
		return nil, errors.New("set an ACME account email in Cloudflare settings")
	}
	dir := lego.LEDirectoryProduction
	if staging {
		dir = lego.LEDirectoryStaging
	}
	acct, err := i.loadAccount(ctx, email, dir)
	if err != nil {
		return nil, err
	}
	cfg := lego.NewConfig(acct)
	cfg.CADirURL = dir
	cfg.Certificate.KeyType = certcrypto.EC256
	if keyType == "rsa2048" {
		cfg.Certificate.KeyType = certcrypto.RSA2048
	}
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	pc := cloudflare.NewDefaultConfig()
	pc.AuthToken = cfToken
	pc.TTL = 120
	pc.PropagationTimeout = 3 * time.Minute
	pc.PollingInterval = 5 * time.Second
	prov, err := cloudflare.NewDNSProviderConfig(pc)
	if err != nil {
		return nil, err
	}
	if err := client.Challenge.SetDNS01Provider(prov, dns01.AddRecursiveNameservers([]string{"1.1.1.1:53", "1.0.0.1:53"})); err != nil {
		return nil, err
	}
	if acct.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("acme register: %w", err)
		}
		acct.Registration = reg
		if err := i.saveAccount(ctx, acct); err != nil {
			return nil, err
		}
	}
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: domains, Bundle: true})
	if err != nil {
		return nil, err
	}
	notAfter, issuer, err := ParseCert(res.Certificate)
	if err != nil {
		return nil, err
	}
	return &Result{CertPEM: string(res.Certificate), KeyPEM: string(res.PrivateKey), NotAfter: notAfter, Issuer: issuer}, nil
}

func (i *Issuer) loadAccount(ctx context.Context, email, dir string) (*account, error) {
	var a account
	key := "acme_account"
	err := i.Store.GetKV(ctx, key, &a)
	if err == nil && a.Email == email && a.Directory == dir {
		blk, _ := pem.Decode([]byte(a.KeyPEM))
		if blk != nil {
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				a.key = k
				return &a, nil
			}
		}
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, _ := x509.MarshalECPrivateKey(k)
	return &account{Email: email, Directory: dir, key: k,
		KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))}, nil
}

func (i *Issuer) saveAccount(ctx context.Context, a *account) error {
	b, _ := json.Marshal(a)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return i.Store.SetKV(ctx, "acme_account", m)
}

// ParseCert returns the expiry and issuer CN of the first certificate in a PEM bundle.
func ParseCert(p []byte) (time.Time, string, error) {
	blk, _ := pem.Decode(p)
	if blk == nil {
		return time.Time{}, "", errors.New("no PEM certificate found")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}, "", err
	}
	issuer := c.Issuer.CommonName
	if len(c.Issuer.Organization) > 0 {
		issuer = c.Issuer.Organization[0] + " " + c.Issuer.CommonName
	}
	return c.NotAfter, issuer, nil
}

// ParseCertDomains returns the DNS names in the first certificate.
func ParseCertDomains(p []byte) ([]string, error) {
	blk, _ := pem.Decode(p)
	if blk == nil {
		return nil, errors.New("no PEM certificate found")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, err
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames, nil
	}
	return []string{c.Subject.CommonName}, nil
}
