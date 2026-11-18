package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

// RenewBefore is how long before expiry ACME certificates are renewed.
const RenewBefore = 30 * 24 * time.Hour

// IssueCertificate obtains (or renews) an ACME certificate in the background
// and applies the config when it succeeds.
func (e *Engine) IssueCertificate(id string) error {
	if _, busy := e.issuing.LoadOrStore(id, true); busy {
		return fmt.Errorf("issuance already running for this certificate")
	}
	go func() {
		defer e.issuing.Delete(id)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := e.issue(ctx, id); err != nil {
			e.Log.Warn("certificate issuance failed", "id", id, "err", err)
		}
	}()
	return nil
}

func (e *Engine) issue(ctx context.Context, id string) error {
	var c model.Certificate
	if err := e.Store.Get(ctx, store.KindCert, id, &c); err != nil {
		return err
	}
	cf, err := e.Store.Cloudflare(ctx)
	if err != nil {
		return err
	}
	if c.CertPEM != "" {
		c.Status = "renewing"
	} else {
		c.Status = "pending"
	}
	c.LastError = ""
	_ = e.Store.Put(ctx, store.KindCert, id, c)
	res, err := e.Issuer.Obtain(ctx, cf.ACMEEmail, cf.APIToken, cf.ACMEStaging, c.KeyType, c.Domains)
	// Re-read in case the record changed while we waited.
	var cur model.Certificate
	if gerr := e.Store.Get(ctx, store.KindCert, id, &cur); gerr != nil {
		return gerr
	}
	if err != nil {
		cur.LastError = err.Error()
		cur.UpdatedAt = time.Now().UTC()
		if cur.CertPEM != "" {
			cur.Status = "valid"
		} else {
			cur.Status = "failed"
		}
		_ = e.Store.Put(ctx, store.KindCert, id, cur)
		e.Store.AddEvent(ctx, "error", "certificate", fmt.Sprintf("Issuing %s failed: %v", first(cur.Domains), err))
		return err
	}
	cur.CertPEM, cur.KeyPEM, cur.NotAfter, cur.Issuer = res.CertPEM, res.KeyPEM, res.NotAfter, res.Issuer
	cur.Status, cur.LastError, cur.UpdatedAt = "valid", "", time.Now().UTC()
	if err := e.Store.Put(ctx, store.KindCert, id, cur); err != nil {
		return err
	}
	e.Store.AddEvent(ctx, "info", "certificate", fmt.Sprintf("Issued %s via DNS-01, valid until %s", first(cur.Domains), cur.NotAfter.Format("2 Jan 2006")))
	if e.Cfg.IsPrimary() {
		_, err = e.Apply(ctx, "system", "Certificate issued for "+first(cur.Domains))
	}
	return err
}

// RenewDue renews ACME certificates that expire within RenewBefore.
func (e *Engine) RenewDue(ctx context.Context) {
	if !e.Cfg.IsPrimary() {
		return
	}
	certs, err := e.Store.Certs(ctx)
	if err != nil {
		return
	}
	for _, c := range certs {
		if c.Source != "acme" {
			continue
		}
		if c.CertPEM == "" || time.Until(c.NotAfter) < RenewBefore {
			if c.Status == "failed" && time.Since(c.UpdatedAt) < 6*time.Hour {
				continue
			}
			_ = e.IssueCertificate(c.ID)
		}
	}
}

// Issuing reports whether a certificate is being issued right now.
func (e *Engine) Issuing(id string) bool {
	_, ok := e.issuing.Load(id)
	return ok
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
