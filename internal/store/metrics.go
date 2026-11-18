package store

import (
	"context"
	"strings"
	"time"

	"github.com/joshuadouch/halyard/internal/model"
)

// MetricRow is one stored metrics sample: tier is its resolution in seconds.
type MetricRow struct {
	T    int64
	Data string
}

// SaveMetric stores a sample and drops samples of the same tier older than keepSince.
func (s *Store) SaveMetric(ctx context.Context, tier int, t int64, data []byte, keepSince int64) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO metrics(tier,t,data) VALUES(?,?,?)
	  ON CONFLICT(tier,t) DO UPDATE SET data=excluded.data`, tier, t, string(data)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM metrics WHERE tier=? AND t<?`, tier, keepSince)
	return err
}

// Metrics returns samples of one tier at or after since, oldest first.
func (s *Store) Metrics(ctx context.Context, tier int, since int64) ([]MetricRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t,data FROM metrics WHERE tier=? AND t>=? ORDER BY t`, tier, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricRow
	for rows.Next() {
		var r MetricRow
		if err := rows.Scan(&r.T, &r.Data); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventsOfKind returns the latest events of the given kinds, newest first.
func (s *Store) EventsOfKind(ctx context.Context, limit int, kinds ...string) ([]model.Event, error) {
	if len(kinds) == 0 {
		return s.Events(ctx, limit)
	}
	args := []any{}
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id,time,level,kind,message FROM events WHERE kind IN (?`+
		strings.Repeat(",?", len(kinds)-1)+`) ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
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
