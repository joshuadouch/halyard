package engine

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

// SyncPayload is what the primary sends to standby nodes.
type SyncPayload struct {
	From       string                   `json:"from"`
	Time       time.Time                `json:"time"`
	Snapshot   store.Snapshot           `json:"snapshot"`
	Cloudflare model.CloudflareSettings `json:"cloudflare"`
}

// PeerStatus is the latest sync result for a peer.
type PeerStatus struct {
	Name    string    `json:"name"`
	URL     string    `json:"url"`
	Address string    `json:"address"`
	OK      bool      `json:"ok"`
	Time    time.Time `json:"time"`
	Error   string    `json:"error"`
}

func syncKey(secret string) []byte {
	k := sha256.Sum256([]byte("halyard-sync:" + secret))
	return k[:]
}

// Seal encrypts and authenticates a payload with the cluster secret.
func Seal(secret string, plain []byte) ([]byte, error) {
	blk, err := aes.NewCipher(syncKey(secret))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

// Open decrypts a sealed payload.
func Open(secret string, sealed []byte) ([]byte, error) {
	blk, err := aes.NewCipher(syncKey(secret))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("payload too short")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}

// SyncPeers pushes the current state to every peer.
func (e *Engine) SyncPeers(ctx context.Context) {
	if len(e.Cfg.Cluster.Peers) == 0 || !e.Cfg.IsPrimary() {
		return
	}
	snap, err := e.Store.TakeSnapshot(ctx)
	if err != nil {
		return
	}
	cf, _ := e.Store.Cloudflare(ctx)
	body, _ := json.Marshal(SyncPayload{From: e.Cfg.Node.Name, Time: time.Now().UTC(), Snapshot: snap, Cloudflare: cf})
	sealed, err := Seal(e.Cfg.Cluster.Secret, body)
	if err != nil {
		return
	}
	// Peers use self-signed management certs; the payload itself is
	// encrypted and authenticated with the cluster secret.
	hc := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	for _, p := range e.Cfg.Cluster.Peers {
		st := PeerStatus{Name: p.Name, URL: p.URL, Address: p.Address, Time: time.Now().UTC()}
		err := func() error {
			req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(p.URL, "/")+"/api/cluster/sync", bytes.NewReader(sealed))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/octet-stream")
			resp, err := hc.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if resp.StatusCode != 200 {
				return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
			}
			return nil
		}()
		if err != nil {
			st.Error = err.Error()
			e.Store.AddEvent(ctx, "warn", "cluster", fmt.Sprintf("Sync to %s failed: %v", p.Name, err))
		} else {
			st.OK = true
		}
		e.lastSync.Store(p.Name, st)
	}
}

// PeerStatuses returns the latest sync result per peer.
func (e *Engine) PeerStatuses() []PeerStatus {
	var out []PeerStatus
	for _, p := range e.Cfg.Cluster.Peers {
		if v, ok := e.lastSync.Load(p.Name); ok {
			out = append(out, v.(PeerStatus))
		} else {
			out = append(out, PeerStatus{Name: p.Name, URL: p.URL, Address: p.Address})
		}
	}
	return out
}

// ReceiveSync applies a payload from the primary on a standby node.
func (e *Engine) ReceiveSync(ctx context.Context, sealed []byte) error {
	if e.Cfg.Cluster.Secret == "" {
		return errors.New("cluster secret not configured")
	}
	if e.Cfg.IsPrimary() {
		return errors.New("this node is a primary and does not accept sync")
	}
	plain, err := Open(e.Cfg.Cluster.Secret, sealed)
	if err != nil {
		return errors.New("payload could not be authenticated")
	}
	var p SyncPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		return err
	}
	if d := time.Since(p.Time); d > 5*time.Minute || d < -5*time.Minute {
		return errors.New("payload is too old or clocks are out of sync")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.Store.RestoreSnapshot(ctx, p.Snapshot, true); err != nil {
		return err
	}
	local, _ := e.Store.Cloudflare(ctx)
	p.Cloudflare.LastReconcile = local.LastReconcile
	if err := e.Store.SetCloudflare(ctx, p.Cloudflare); err != nil {
		return err
	}
	if _, err := e.applyLocked(ctx, "sync from "+p.From, "Config synced from "+p.From, false); err != nil {
		return err
	}
	go func() {
		if err := e.WriteTunnelToken(context.Background()); err != nil {
			e.Log.Warn("tunnel token", "err", err)
		}
	}()
	return nil
}
