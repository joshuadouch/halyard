// Package store persists Halyard state in a single SQLite file.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/joshuadouch/halyard/internal/model"
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("not found")

// Kinds of objects kept in the generic objects table.
const (
	KindVIP  = "vip"
	KindHost = "host"
	KindPool = "pool"
	KindCert = "cert"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS objects (
  kind TEXT NOT NULL, id TEXT NOT NULL, data TEXT NOT NULL, updated_at TEXT NOT NULL,
  PRIMARY KEY (kind, id));
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY, username TEXT UNIQUE NOT NULL, email TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '', totp_secret TEXT NOT NULL DEFAULT '', totp_enabled INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY, user_id TEXT NOT NULL, expires_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, time TEXT NOT NULL, level TEXT NOT NULL, kind TEXT NOT NULL, message TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS revisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT, time TEXT NOT NULL, author TEXT NOT NULL, message TEXT NOT NULL,
  haproxy TEXT NOT NULL, keepalived TEXT NOT NULL, snapshot TEXT NOT NULL, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '');
`

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// NewID returns a short random identifier.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Put stores an object as JSON.
func (s *Store) Put(ctx context.Context, kind, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO objects(kind,id,data,updated_at) VALUES(?,?,?,?)
	  ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`, kind, id, string(b), now())
	return err
}

// Get loads an object into v.
func (s *Store) Get(ctx context.Context, kind, id string, v any) error {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM objects WHERE kind=? AND id=?`, kind, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), v)
}

// Delete removes an object.
func (s *Store) Delete(ctx context.Context, kind, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM objects WHERE kind=? AND id=?`, kind, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func listRaw(ctx context.Context, db *sql.DB, kind string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT data FROM objects WHERE kind=? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func list[T any](ctx context.Context, s *Store, kind string) ([]T, error) {
	raw, err := listRaw(ctx, s.db, kind)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(raw))
	for _, r := range raw {
		var v T
		if err := json.Unmarshal([]byte(r), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// VIPs lists VIPs ordered by VRID.
func (s *Store) VIPs(ctx context.Context) ([]model.VIP, error) {
	v, err := list[model.VIP](ctx, s, KindVIP)
	sort.Slice(v, func(i, j int) bool { return v[i].VRID < v[j].VRID })
	return v, err
}

// Hosts lists hosts ordered by first hostname.
func (s *Store) Hosts(ctx context.Context) ([]model.Host, error) {
	v, err := list[model.Host](ctx, s, KindHost)
	sort.Slice(v, func(i, j int) bool { return first(v[i].Hostnames) < first(v[j].Hostnames) })
	return v, err
}

// Pools lists pools ordered by name.
func (s *Store) Pools(ctx context.Context) ([]model.Pool, error) {
	v, err := list[model.Pool](ctx, s, KindPool)
	sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	return v, err
}

// Certs lists certificates (with keys).
func (s *Store) Certs(ctx context.Context) ([]model.Certificate, error) {
	v, err := list[model.Certificate](ctx, s, KindCert)
	sort.Slice(v, func(i, j int) bool { return first(v[i].Domains) < first(v[j].Domains) })
	return v, err
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// KV helpers -------------------------------------------------------------

// GetKV loads a JSON value; returns ErrNotFound when missing.
func (s *Store) GetKV(ctx context.Context, key string, v any) error {
	var d string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key=?`, key).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(d), v)
}

// SetKV stores a JSON value.
func (s *Store) SetKV(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(b))
	return err
}

// Cloudflare returns Cloudflare settings (zero value if unset).
func (s *Store) Cloudflare(ctx context.Context) (model.CloudflareSettings, error) {
	var c model.CloudflareSettings
	err := s.GetKV(ctx, "cloudflare", &c)
	if errors.Is(err, ErrNotFound) {
		return model.CloudflareSettings{RealIP: true}, nil
	}
	return c, err
}

// SetCloudflare saves Cloudflare settings.
func (s *Store) SetCloudflare(ctx context.Context, c model.CloudflareSettings) error {
	return s.SetKV(ctx, "cloudflare", c)
}

// Events -----------------------------------------------------------------

// AddEvent appends an activity event.
func (s *Store) AddEvent(ctx context.Context, level, kind, msg string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO events(time,level,kind,message) VALUES(?,?,?,?)`, now(), level, kind, msg)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM events WHERE id < (SELECT COALESCE(MAX(id),0)-2000 FROM events)`)
}

// Events returns the latest events, newest first.
func (s *Store) Events(ctx context.Context, limit int) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,time,level,kind,message FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var t string
		if err := rows.Scan(&e.ID, &t, &e.Level, &e.Kind, &e.Message); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, t)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Revisions --------------------------------------------------------------

// AddRevision records an apply attempt and returns its id.
func (s *Store) AddRevision(ctx context.Context, r model.Revision) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO revisions(time,author,message,haproxy,keepalived,snapshot,status,error) VALUES(?,?,?,?,?,?,?,?)`,
		now(), r.Author, r.Message, r.HAProxy, r.Keepalive, r.Snapshot, r.Status, r.Error)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Revisions lists revisions without bodies, newest first.
func (s *Store) Revisions(ctx context.Context, limit int) ([]model.Revision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,time,author,message,status,error FROM revisions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Revision
	for rows.Next() {
		var r model.Revision
		var t string
		if err := rows.Scan(&r.ID, &t, &r.Author, &r.Message, &r.Status, &r.Error); err != nil {
			return nil, err
		}
		r.Time, _ = time.Parse(time.RFC3339Nano, t)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Revision loads one revision with bodies.
func (s *Store) Revision(ctx context.Context, id int64) (model.Revision, error) {
	var r model.Revision
	var t string
	err := s.db.QueryRowContext(ctx, `SELECT id,time,author,message,haproxy,keepalived,snapshot,status,error FROM revisions WHERE id=?`, id).
		Scan(&r.ID, &t, &r.Author, &r.Message, &r.HAProxy, &r.Keepalive, &r.Snapshot, &r.Status, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.Time, _ = time.Parse(time.RFC3339Nano, t)
	return r, err
}

// LastApplied returns the latest successfully applied revision.
func (s *Store) LastApplied(ctx context.Context) (model.Revision, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM revisions WHERE status='applied' ORDER BY id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Revision{}, ErrNotFound
	}
	if err != nil {
		return model.Revision{}, err
	}
	return s.Revision(ctx, id)
}

// Snapshot is every object, used for revisions, rollback and cluster sync.
type Snapshot struct {
	VIPs  []model.VIP         `json:"vips"`
	Hosts []model.Host        `json:"hosts"`
	Pools []model.Pool        `json:"pools"`
	Certs []model.Certificate `json:"certs"`
}

// TakeSnapshot reads all objects.
func (s *Store) TakeSnapshot(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	var err error
	if snap.VIPs, err = s.VIPs(ctx); err != nil {
		return snap, err
	}
	if snap.Hosts, err = s.Hosts(ctx); err != nil {
		return snap, err
	}
	if snap.Pools, err = s.Pools(ctx); err != nil {
		return snap, err
	}
	if snap.Certs, err = s.Certs(ctx); err != nil {
		return snap, err
	}
	return snap, nil
}

// RestoreSnapshot replaces VIPs, hosts and pools (and optionally certs).
func (s *Store) RestoreSnapshot(ctx context.Context, snap Snapshot, withCerts bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	kinds := []string{KindVIP, KindHost, KindPool}
	if withCerts {
		kinds = append(kinds, KindCert)
	}
	for _, k := range kinds {
		if _, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE kind=?`, k); err != nil {
			return err
		}
	}
	put := func(kind, id string, v any) error {
		b, _ := json.Marshal(v)
		_, err := tx.ExecContext(ctx, `INSERT INTO objects(kind,id,data,updated_at) VALUES(?,?,?,?)`, kind, id, string(b), now())
		return err
	}
	for _, v := range snap.VIPs {
		if err := put(KindVIP, v.ID, v); err != nil {
			return err
		}
	}
	for _, v := range snap.Hosts {
		if err := put(KindHost, v.ID, v); err != nil {
			return err
		}
	}
	for _, v := range snap.Pools {
		if err := put(KindPool, v.ID, v); err != nil {
			return err
		}
	}
	if withCerts {
		for _, v := range snap.Certs {
			if err := put(KindCert, v.ID, v); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Users & sessions -------------------------------------------------------

// CountUsers returns how many users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// SaveUser inserts or updates a user.
func (s *Store) SaveUser(ctx context.Context, u model.User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id,username,email,password_hash,totp_secret,totp_enabled) VALUES(?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET username=excluded.username,email=excluded.email,password_hash=excluded.password_hash,
	  totp_secret=excluded.totp_secret,totp_enabled=excluded.totp_enabled`,
		u.ID, u.Username, u.Email, u.PasswordHash, u.TOTPSecret, boolInt(u.TOTPEnabled))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) userWhere(ctx context.Context, where string, arg any) (model.User, error) {
	var u model.User
	var totp int
	err := s.db.QueryRowContext(ctx, `SELECT id,username,email,password_hash,totp_secret,totp_enabled FROM users WHERE `+where, arg).
		Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.TOTPSecret, &totp)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.TOTPEnabled = totp == 1
	return u, err
}

// UserByName looks a user up by username.
func (s *Store) UserByName(ctx context.Context, name string) (model.User, error) {
	return s.userWhere(ctx, "username=?", name)
}

// UserByEmail looks a user up by email (case-insensitive).
func (s *Store) UserByEmail(ctx context.Context, email string) (model.User, error) {
	return s.userWhere(ctx, "lower(email)=lower(?) AND email<>''", email)
}

// UserByID looks a user up by id.
func (s *Store) UserByID(ctx context.Context, id string) (model.User, error) {
	return s.userWhere(ctx, "id=?", id)
}

// Users lists users.
func (s *Store) Users(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,username,email,totp_enabled FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var u model.User
		var t int
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &t); err != nil {
			return nil, err
		}
		u.TOTPEnabled = t == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes a user and their sessions.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id)
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// CreateSession stores a session token.
func (s *Store) CreateSession(ctx context.Context, token, userID string, ttl time.Duration) error {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token,user_id,expires_at) VALUES(?,?,?)`,
		token, userID, time.Now().Add(ttl).UTC().Format(time.RFC3339Nano))
	return err
}

// SessionUser returns the user for a valid session token.
func (s *Store) SessionUser(ctx context.Context, token string) (model.User, error) {
	var uid, exp string
	err := s.db.QueryRowContext(ctx, `SELECT user_id,expires_at FROM sessions WHERE token=?`, token).Scan(&uid, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}
	if t, _ := time.Parse(time.RFC3339Nano, exp); time.Now().After(t) {
		return model.User{}, ErrNotFound
	}
	return s.UserByID(ctx, uid)
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, token string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token=?`, token)
}
