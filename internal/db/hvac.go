package db

import (
	"context"
	"time"
)

// InsertHVACSample stores one raw hvac_action sample from the HA WebSocket
// (e.g. "heating", "cooling", "idle", "off"). Watts are derived at query time
// from the hvac_*_kw settings, so retuning the estimate is retroactive. src
// defaults to 'live'.
func (d *DB) InsertHVACSample(ctx context.Context, entity string, ts time.Time, action string) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO hvac_samples (ts, entity_id, action) VALUES ($1,$2,$3)`,
		ts, entity, action)
	return err
}

// ReplaceHVACBackfill idempotently (re)writes history-derived hvac_action
// samples for one entity over [from,to]: prior history_backfill rows in the
// span are deleted first; live rows are never touched. Mirrors
// ReplaceBackfillSamples (reference.go).
func (d *DB) ReplaceHVACBackfill(ctx context.Context, entity string, from, to time.Time, ts []time.Time, actions []string) error {
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM hvac_samples WHERE entity_id=$1 AND src='history_backfill' AND ts >= $2 AND ts <= $3`,
		entity, from, to); err != nil {
		return err
	}
	if len(ts) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
INSERT INTO hvac_samples (ts, entity_id, action, src)
SELECT t, $1, a, 'history_backfill' FROM unnest($2::timestamptz[], $3::text[]) AS u(t, a)`,
		entity, ts, actions)
	return err
}

// sampleGaps returns holes (> minGap with zero samples) in the last
// `lookback` of a samples table, including a trailing hole from the last
// sample to capEnd. entityWhere is the caller's own $1 predicate (e.g.
// "entity_id = ANY($1)" or "entity_id = $1") bound to entityArg; table names
// cannot be SQL parameters, so table is concatenated directly and must be a
// trusted, internal identifier. Shared by ReferenceGaps (reference.go) and
// HVACGaps so the two queries stay identical.
func (d *DB) sampleGaps(ctx context.Context, table, entityWhere string, entityArg any, lookback, minGap time.Duration, capEnd time.Time) [][2]time.Time {
	out := [][2]time.Time{}
	rows, err := d.pool.Query(ctx, `
WITH mins AS (
  SELECT DISTINCT time_bucket('1 minute', ts) AS mt
  FROM `+table+`
  WHERE `+entityWhere+` AND ts >= $2),
g AS (SELECT mt, lag(mt) OVER (ORDER BY mt) AS prev FROM mins)
SELECT prev, mt FROM g WHERE mt - prev > make_interval(secs => $3) ORDER BY prev`,
		entityArg, time.Now().Add(-lookback), minGap.Seconds())
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a, b time.Time
		if err := rows.Scan(&a, &b); err != nil {
			return out
		}
		out = append(out, [2]time.Time{a, b})
	}
	rows.Close()
	// trailing hole: feed dead right now (or backfill catching up after restart)
	var last *time.Time
	_ = d.pool.QueryRow(ctx, `SELECT max(ts) FROM `+table+` WHERE `+entityWhere, entityArg).Scan(&last)
	if last != nil && capEnd.Sub(*last) > minGap {
		out = append(out, [2]time.Time{*last, capEnd})
	}
	return out
}

// HVACGaps returns holes (> minGap with zero samples) in the last `lookback`
// of hvac_samples for one entity, including a trailing hole from the last
// sample to capEnd — the work-list for history backfill. Same semantics as
// ReferenceGaps.
func (d *DB) HVACGaps(ctx context.Context, entity string, lookback, minGap time.Duration, capEnd time.Time) [][2]time.Time {
	if entity == "" {
		return [][2]time.Time{}
	}
	return d.sampleGaps(ctx, "hvac_samples", "entity_id = $1", entity, lookback, minGap, capEnd)
}

// HVACSpan returns the first and last sample ts for entity (nil, nil if none
// exist yet). The backfill loop uses this to find the leading gap: a freshly
// configured entity gets a seed sample immediately on connect, so the
// interesting hole is BEFORE the first sample, not one HVACGaps' interior/
// trailing search already covers.
func (d *DB) HVACSpan(ctx context.Context, entity string) (first, last *time.Time) {
	_ = d.pool.QueryRow(ctx,
		`SELECT min(ts), max(ts) FROM hvac_samples WHERE entity_id=$1`, entity).Scan(&first, &last)
	return first, last
}

// HVACStatus returns the latest hvac_action sample's action and ts for
// entity ("", nil if none).
func (d *DB) HVACStatus(ctx context.Context, entity string) (action string, ts *time.Time) {
	var a *string
	var t *time.Time
	_ = d.pool.QueryRow(ctx,
		`SELECT action, ts FROM hvac_samples WHERE entity_id=$1 ORDER BY ts DESC LIMIT 1`, entity).Scan(&a, &t)
	if a != nil {
		action = *a
	}
	return action, t
}
