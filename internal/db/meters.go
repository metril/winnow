package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"winnow/internal/ert"
	"winnow/internal/model"
)

// LeaderboardOpts filters the meter list.
type LeaderboardOpts struct {
	Since, Until   *time.Time
	MsgType        string
	ElectricOnly   bool
	IncludeIgnored bool
	TrackedOnly    bool
	PublishedOnly  bool
}

// Leaderboard returns per-meter summaries joined with annotations. Windowed
// stats come from the readings_1m continuous aggregate; metadata and all-time
// latest from the meter_index registry — so it never scans raw readings.
func (d *DB) Leaderboard(ctx context.Context, o LeaderboardOpts) ([]model.Meter, error) {
	where := "WHERE TRUE"
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where += fmt.Sprintf(" AND %s$%d", cond, len(args))
	}
	if o.Since != nil {
		add("w.bucket >= ", *o.Since)
	}
	if o.Until != nil {
		add("w.bucket <= ", *o.Until)
	}

	// total_movement is the sum of rollover-aware, glitch-filtered hourly deltas —
	// not max−min, which a single bit-flipped decode (e.g. a +2^17 counter jump)
	// inflates by orders of magnitude. The delta ladder reads readings_1h (the
	// shared basis, which also carries the min-evidence rule that dashes out
	// briefly-heard meters); packets/first/last stay on readings_1m for minute
	// fidelity on "last seen".
	q := `
WITH win AS (
  SELECT endpoint_id,
         sum(n)      AS packets,
         min(bucket) AS first_seen,
         max(bucket) AS last_seen
  FROM readings_1m w
  ` + where + `
  GROUP BY endpoint_id),` +
		hourlyDeltaCTEs("TRUE"+strings.ReplaceAll(strings.TrimPrefix(where, "WHERE TRUE"), "w.bucket", "r.bucket")) + `,
mv AS (SELECT endpoint_id, sum(delta) AS total_movement FROM glitch_clean GROUP BY endpoint_id)
SELECT win.endpoint_id, i.msg_type, i.endpoint_type,
       win.packets,
       COALESCE((SELECT count(*) FROM meter_source ms WHERE ms.endpoint_id = win.endpoint_id), 0) AS sources,
       win.first_seen, win.last_seen, mv.total_movement, i.last_consumption,
       m.label, m.notes, m.is_candidate, m.is_mine, m.ignored, m.publish,
       m.pub_name, m.pub_multiplier, m.pub_unit
FROM win
JOIN meter_index i ON i.endpoint_id = win.endpoint_id
LEFT JOIN mv ON mv.endpoint_id = win.endpoint_id
LEFT JOIN meters m ON m.endpoint_id = win.endpoint_id`
	if o.MsgType != "" {
		args = append(args, o.MsgType)
		q += fmt.Sprintf(" WHERE i.msg_type = $%d", len(args))
	}

	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Meter{}
	for rows.Next() {
		var m model.Meter
		var first, last time.Time
		var isCand, isMine, ignored, publish *bool
		var mult *float64
		if err := rows.Scan(&m.EndpointID, &m.MsgType, &m.EndpointType, &m.Packets,
			&m.Sources, &first, &last, &m.TotalMovement, &m.LatestConsumption,
			&m.Label, &m.Notes, &isCand, &isMine, &ignored, &publish,
			&m.PubName, &mult, &m.PubUnit); err != nil {
			return nil, err
		}
		m.Commodity = ert.Commodity(m.EndpointType)
		m.FirstSeen = first.UTC().Format(time.RFC3339Nano)
		m.LastSeen = last.UTC().Format(time.RFC3339Nano)
		m.IsCandidate = isCand != nil && *isCand
		m.IsMine = isMine != nil && *isMine
		m.Ignored = ignored != nil && *ignored
		m.Publish = publish != nil && *publish
		m.PubMultiplier = 1
		if mult != nil {
			m.PubMultiplier = *mult
		}
		hours := last.Sub(first).Hours()
		if hours <= 0 {
			hours = 1.0 / 3600
		}
		m.PacketsPerHour = round(float64(m.Packets)/hours, 2)

		// flag filters
		if !o.IncludeIgnored && m.Ignored {
			continue
		}
		if o.TrackedOnly && !m.IsMine {
			continue
		}
		if o.PublishedOnly && !m.Publish {
			continue
		}
		if o.ElectricOnly && m.Commodity != "electric" {
			continue
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		return deref(out[i].TotalMovement) > deref(out[j].TotalMovement)
	})
	return out, nil
}

// GetMeter returns the annotation row (or zero values) for a meter.
func (d *DB) GetMeter(ctx context.Context, id int64) (model.Meter, error) {
	m := model.Meter{EndpointID: id, PubMultiplier: 1}
	var isCand, isMine, ignored, publish *bool
	var mult *float64
	err := d.pool.QueryRow(ctx,
		`SELECT label, notes, is_candidate, is_mine, ignored, publish, pub_name, pub_multiplier, pub_unit
		 FROM meters WHERE endpoint_id=$1`, id).
		Scan(&m.Label, &m.Notes, &isCand, &isMine, &ignored, &publish, &m.PubName, &mult, &m.PubUnit)
	if err != nil {
		// no row yet is fine
		return m, nil
	}
	m.IsCandidate = isCand != nil && *isCand
	m.IsMine = isMine != nil && *isMine
	m.Ignored = ignored != nil && *ignored
	m.Publish = publish != nil && *publish
	if mult != nil {
		m.PubMultiplier = *mult
	}
	return m, nil
}

// MeterInfo is GetMeter plus the registry metadata (meter_index), so a meter
// with no packets in the queried window still shows what it is — the detail
// header used to render empty msg_type/packets for out-of-window meters.
func (d *DB) MeterInfo(ctx context.Context, id int64) (model.Meter, error) {
	m, err := d.GetMeter(ctx, id)
	if err != nil {
		return m, err
	}
	var msgType *string
	var first, last *time.Time
	_ = d.pool.QueryRow(ctx, `
SELECT msg_type, endpoint_type, packets, first_seen, last_seen, last_consumption
FROM meter_index WHERE endpoint_id = $1`, id).
		Scan(&msgType, &m.EndpointType, &m.Packets, &first, &last, &m.LatestConsumption)
	if msgType != nil {
		m.MsgType = *msgType
	}
	if m.EndpointType != nil {
		m.Commodity = ert.Commodity(m.EndpointType)
	}
	if first != nil {
		m.FirstSeen = first.UTC().Format(time.RFC3339)
	}
	if last != nil {
		m.LastSeen = last.UTC().Format(time.RFC3339)
	}
	return m, nil
}

// UpdateMeter upserts annotation/flag fields. Only non-nil fields are changed.
func (d *DB) UpdateMeter(ctx context.Context, id int64, f MeterUpdate) (model.Meter, error) {
	if _, err := d.pool.Exec(ctx,
		`INSERT INTO meters (endpoint_id) VALUES ($1) ON CONFLICT DO NOTHING`, id); err != nil {
		return model.Meter{}, err
	}
	sets, args := []string{}, []any{}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s=$%d", col, len(args)))
	}
	if f.Label != nil {
		set("label", *f.Label)
	}
	if f.Notes != nil {
		set("notes", *f.Notes)
	}
	if f.IsCandidate != nil {
		set("is_candidate", *f.IsCandidate)
	}
	if f.IsMine != nil {
		set("is_mine", *f.IsMine)
	}
	if f.Ignored != nil {
		set("ignored", *f.Ignored)
	}
	if f.Publish != nil {
		set("publish", *f.Publish)
	}
	if f.PubName != nil {
		set("pub_name", *f.PubName)
	}
	if f.PubMultiplier != nil {
		set("pub_multiplier", *f.PubMultiplier)
	}
	if f.PubUnit != nil {
		set("pub_unit", *f.PubUnit)
	}
	if len(sets) > 0 {
		args = append(args, id)
		q := "UPDATE meters SET " + joinComma(sets) + fmt.Sprintf(" WHERE endpoint_id=$%d", len(args))
		if _, err := d.pool.Exec(ctx, q, args...); err != nil {
			return model.Meter{}, err
		}
	}
	return d.GetMeter(ctx, id)
}

// DeleteMeter removes a meter's annotation row (untrack). When purge is true it
// also deletes all stored readings and registry rows for the endpoint, so the
// meter disappears entirely (it reappears only if it broadcasts again).
func (d *DB) DeleteMeter(ctx context.Context, id int64, purge bool) error {
	if _, err := d.pool.Exec(ctx, `DELETE FROM meters WHERE endpoint_id=$1`, id); err != nil {
		return err
	}
	if purge {
		for _, q := range []string{
			`DELETE FROM readings WHERE endpoint_id=$1`,
			`DELETE FROM meter_index WHERE endpoint_id=$1`,
			`DELETE FROM meter_source WHERE endpoint_id=$1`,
		} {
			if _, err := d.pool.Exec(ctx, q, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// MeterUpdate carries optional annotation/flag changes (nil = unchanged).
type MeterUpdate struct {
	Label         *string
	Notes         *string
	IsCandidate   *bool
	IsMine        *bool
	Ignored       *bool
	Publish       *bool
	PubName       *string
	PubMultiplier *float64
	PubUnit       *string
}

// MetersForPublish returns meters flagged publish=true with their pub config.
func (d *DB) MetersForPublish(ctx context.Context) ([]model.Meter, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT endpoint_id, pub_name, pub_multiplier, pub_unit FROM meters WHERE publish = TRUE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Meter{}
	for rows.Next() {
		m := model.Meter{Publish: true, PubMultiplier: 1}
		var mult *float64
		if err := rows.Scan(&m.EndpointID, &m.PubName, &mult, &m.PubUnit); err != nil {
			return nil, err
		}
		if mult != nil {
			m.PubMultiplier = *mult
		}
		// fill msg_type + endpoint_type/commodity for discovery metadata
		_ = d.pool.QueryRow(ctx,
			`SELECT msg_type, endpoint_type FROM readings WHERE endpoint_id=$1 AND msg_type IS NOT NULL ORDER BY ts DESC LIMIT 1`,
			m.EndpointID).Scan(&m.MsgType, &m.EndpointType)
		m.Commodity = ert.Commodity(m.EndpointType)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MeterReadings returns raw readings for a meter (CSV export).
func (d *DB) MeterReadings(ctx context.Context, id int64, since, until *time.Time) ([]model.Reading, error) {
	where := "WHERE endpoint_id = $1"
	args := []any{id}
	if since != nil {
		args = append(args, *since)
		where += fmt.Sprintf(" AND ts >= $%d", len(args))
	}
	if until != nil {
		args = append(args, *until)
		where += fmt.Sprintf(" AND ts <= $%d", len(args))
	}
	rows, err := d.pool.Query(ctx,
		"SELECT ts, msg_type, endpoint_id, endpoint_type, consumption, source FROM readings "+where+" ORDER BY ts", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Reading
	for rows.Next() {
		var r model.Reading
		var mt *string
		if err := rows.Scan(&r.TS, &mt, &r.EndpointID, &r.EndpointType, &r.Consumption, &r.Source); err != nil {
			return nil, err
		}
		if mt != nil {
			r.MsgType = *mt
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SignalPerHour returns the packet count for a meter over the last hour.
func (d *DB) SignalPerHour(ctx context.Context, id int64) float64 {
	var n int64
	_ = d.pool.QueryRow(ctx,
		`SELECT count(*) FROM readings WHERE endpoint_id=$1 AND ts >= now() - interval '1 hour'`, id).Scan(&n)
	return float64(n)
}

// LatestMsgType returns the most recent message type for a meter.
func (d *DB) LatestMsgType(ctx context.Context, id int64) (string, bool) {
	var mt string
	err := d.pool.QueryRow(ctx,
		`SELECT msg_type FROM readings WHERE endpoint_id=$1 AND msg_type IS NOT NULL ORDER BY ts DESC LIMIT 1`, id).Scan(&mt)
	return mt, err == nil
}

// LatestReading returns the most recent (ts, consumption) for a meter.
func (d *DB) LatestReading(ctx context.Context, id int64) (time.Time, float64, bool) {
	var ts time.Time
	var c float64
	err := d.pool.QueryRow(ctx,
		`SELECT ts, consumption FROM readings WHERE endpoint_id=$1 AND consumption IS NOT NULL ORDER BY ts DESC LIMIT 1`, id).Scan(&ts, &c)
	return ts, c, err == nil
}

// DerivedPower computes instantaneous power from the last two readings of a
// meter, scaled by multiplier (units/hour). Returns (value, ok).
func (d *DB) DerivedPower(ctx context.Context, id int64, multiplier float64) (float64, bool) {
	rows, err := d.pool.Query(ctx,
		`SELECT ts, consumption FROM readings WHERE endpoint_id=$1 AND consumption IS NOT NULL ORDER BY ts DESC LIMIT 2`, id)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	var ts [2]time.Time
	var c [2]float64
	n := 0
	for rows.Next() && n < 2 {
		if err := rows.Scan(&ts[n], &c[n]); err != nil {
			return 0, false
		}
		n++
	}
	if n < 2 {
		return 0, false
	}
	dh := ts[0].Sub(ts[1]).Hours()
	if dh <= 0 {
		return 0, false
	}
	return (c[0] - c[1]) / dh * multiplier, true
}
