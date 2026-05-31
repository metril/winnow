package db

import (
	"context"
	"time"

	"winnow/internal/model"
)

type windowRow struct {
	id         int64
	label      string
	start, end time.Time
}

func (d *DB) closedWindows(ctx context.Context, source string) ([]windowRow, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT id, label, start_ts, end_ts FROM test_windows
		 WHERE end_ts IS NOT NULL AND ($1 = '' OR source = $1) ORDER BY start_ts`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []windowRow
	for rows.Next() {
		var w windowRow
		if err := rows.Scan(&w.id, &w.label, &w.start, &w.end); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func scanTest(row interface {
	Scan(...any) error
}) (model.TestWindow, error) {
	var t model.TestWindow
	var start time.Time
	var end *time.Time
	if err := row.Scan(&t.ID, &t.Label, &start, &end, &t.Source); err != nil {
		return t, err
	}
	t.StartTS = start.UTC().Format(time.RFC3339Nano)
	t.EndTS = iso(end)
	return t, nil
}

// CreateTest inserts a window (end may be nil for a running test).
func (d *DB) CreateTest(ctx context.Context, label string, start time.Time, end *time.Time, source string) (model.TestWindow, error) {
	row := d.pool.QueryRow(ctx,
		`INSERT INTO test_windows (label, start_ts, end_ts, source) VALUES ($1,$2,$3,$4)
		 RETURNING id, label, start_ts, end_ts, source`, label, start, end, source)
	return scanTest(row)
}

// StopTest closes the running window (end_ts = end) if still open.
func (d *DB) StopTest(ctx context.Context, id int64, end time.Time) (model.TestWindow, error) {
	_, _ = d.pool.Exec(ctx, `UPDATE test_windows SET end_ts=$1 WHERE id=$2 AND end_ts IS NULL`, end, id)
	return d.GetTest(ctx, id)
}

func (d *DB) GetTest(ctx context.Context, id int64) (model.TestWindow, error) {
	row := d.pool.QueryRow(ctx,
		`SELECT id, label, start_ts, end_ts, source FROM test_windows WHERE id=$1`, id)
	return scanTest(row)
}

func (d *DB) ListTests(ctx context.Context) ([]model.TestWindow, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT id, label, start_ts, end_ts, source FROM test_windows ORDER BY start_ts DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TestWindow{}
	for rows.Next() {
		t, err := scanTest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) DeleteTest(ctx context.Context, id int64) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM test_windows WHERE id=$1`, id)
	return err
}

// OpenWindow returns the currently-running window for a source, if any.
func (d *DB) OpenWindow(ctx context.Context, source string) (model.TestWindow, bool) {
	row := d.pool.QueryRow(ctx,
		`SELECT id, label, start_ts, end_ts, source FROM test_windows
		 WHERE end_ts IS NULL AND source=$1 ORDER BY start_ts DESC LIMIT 1`, source)
	t, err := scanTest(row)
	return t, err == nil
}
