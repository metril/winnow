package db

import (
	"context"
	"encoding/json"
	"time"

	"winnow/internal/model"
)

// MyMeter returns the meter the dashboard should center on: the one marked
// is_mine, else a labeled candidate (the identification verdict before the user
// confirms). Deliberately independent of the publish flag — viewing your meter
// must not require an MQTT broker.
func (d *DB) MyMeter(ctx context.Context) (model.Meter, bool) {
	m := model.Meter{PubMultiplier: 1}
	var isCand, isMine, ignored, publish *bool
	var mult *float64
	err := d.pool.QueryRow(ctx,
		`SELECT endpoint_id, label, notes, is_candidate, is_mine, ignored, publish, pub_name, pub_multiplier, pub_unit
		 FROM meters
		 WHERE NOT coalesce(ignored, false)
		   AND (is_mine OR (is_candidate AND label IS NOT NULL))
		 ORDER BY is_mine DESC, endpoint_id LIMIT 1`).
		Scan(&m.EndpointID, &m.Label, &m.Notes, &isCand, &isMine, &ignored, &publish, &m.PubName, &mult, &m.PubUnit)
	if err != nil {
		return m, false
	}
	m.IsCandidate = isCand != nil && *isCand
	m.IsMine = isMine != nil && *isMine
	m.Publish = publish != nil && *publish
	if mult != nil {
		m.PubMultiplier = *mult
	}
	return m, true
}

// MovementBetween is a meter's glitch-filtered, rollover-aware consumption rise
// (raw counts) over [start,end) — the honest replacement for the naive max−min
// MovementSince on dashboard surfaces. nil = no usable data in the window.
func (d *DB) MovementBetween(ctx context.Context, id int64, start, end time.Time) *float64 {
	q := `WITH ` + hourlyDeltaCTEs("r.endpoint_id = $1 AND r.bucket >= $2 AND r.bucket < $3") + `
SELECT sum(delta) FROM glitch_clean WHERE hb >= $4`
	var v *float64
	_ = d.pool.QueryRow(ctx, q, id, start.Add(-consumptionLookback), end, start).Scan(&v)
	return v
}

// PubStatus is the worker's last real publish outcome for one meter.
type PubStatus struct {
	LastPublishTS *time.Time `json:"last_publish_ts"`
	LastError     *string    `json:"last_error"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// RecordPublishOK marks a successful MQTT publish (clears any error).
func (d *DB) RecordPublishOK(ctx context.Context, id int64) error {
	_, err := d.pool.Exec(ctx, `
INSERT INTO publish_status (endpoint_id, last_publish_ts, last_error, updated_at)
VALUES ($1, now(), NULL, now())
ON CONFLICT (endpoint_id) DO UPDATE SET last_publish_ts = now(), last_error = NULL, updated_at = now()`, id)
	return err
}

// RecordPublishError stores why a meter's publish is not flowing.
func (d *DB) RecordPublishError(ctx context.Context, id int64, msg string) error {
	_, err := d.pool.Exec(ctx, `
INSERT INTO publish_status (endpoint_id, last_error, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (endpoint_id) DO UPDATE SET last_error = $2, updated_at = now()`, id, msg)
	return err
}

// PublishStatuses returns every recorded publish outcome, keyed by meter.
func (d *DB) PublishStatuses(ctx context.Context) map[int64]PubStatus {
	out := map[int64]PubStatus{}
	rows, err := d.pool.Query(ctx,
		`SELECT endpoint_id, last_publish_ts, last_error, updated_at FROM publish_status`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var s PubStatus
		if err := rows.Scan(&id, &s.LastPublishTS, &s.LastError, &s.UpdatedAt); err != nil {
			return out
		}
		out[id] = s
	}
	return out
}

// WorkerStatus is the worker's own health report (written as a settings row on
// state transitions — the API has no direct line to the worker process).
// HAStream says what the live monitored-power subscription is doing ("ok",
// "reconnecting: …", "entity resolution failed: …"), so a stale-feed banner
// can name the failing stage instead of "check the worker".
type WorkerStatus struct {
	MQTTConnected bool   `json:"mqtt_connected"`
	Detail        string `json:"detail,omitempty"`
	HAStream      string `json:"ha_stream,omitempty"`
	HALastEventTS string `json:"ha_last_event_ts,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}

const workerStatusKey = "worker_status"

// SetWorkerStatus persists the worker's current state (call on transitions).
func (d *DB) SetWorkerStatus(ctx context.Context, ws WorkerStatus) error {
	ws.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(ws)
	if err != nil {
		return err
	}
	return d.SetSetting(ctx, workerStatusKey, string(b))
}

// GetWorkerStatus reads the worker's last reported state (ok=false if never).
func (d *DB) GetWorkerStatus(ctx context.Context) (WorkerStatus, bool) {
	var ws WorkerStatus
	settings, err := d.GetSettings(ctx)
	if err != nil {
		return ws, false
	}
	raw, ok := settings[workerStatusKey]
	if !ok || raw == "" {
		return ws, false
	}
	if json.Unmarshal([]byte(raw), &ws) != nil {
		return ws, false
	}
	return ws, true
}
