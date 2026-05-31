package db

import (
	"context"
	"time"
)

// InsertReferenceSample stores one plug power sample (from the HA WebSocket).
func (d *DB) InsertReferenceSample(ctx context.Context, entity string, ts time.Time, powerW float64) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO reference_samples (ts, entity_id, power_w) VALUES ($1,$2,$3)`,
		ts, entity, powerW)
	return err
}
