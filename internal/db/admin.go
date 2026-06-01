package db

import (
	"context"
	"fmt"
	"time"
)

// adminTables is the fixed set of tables the maintenance page reports on, in a
// sensible display order (biggest/most-interesting first).
var adminTables = []string{
	"readings", "readings_1m", "reference_samples",
	"meter_index", "meter_source", "meters", "test_windows", "sdr_devices",
}

// TableStat is one row in the database-health table.
type TableStat struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Rows  int64  `json:"rows"`
}

// SourceStat is per-dongle reading volume (for delete-by-source + coverage).
type SourceStat struct {
	Source  string `json:"source"`
	Rows    int64  `json:"rows"`
	Bytes   int64  `json:"bytes"` // estimated (rows × avg row width)
	Oldest  string `json:"oldest,omitempty"`
	Newest  string `json:"newest,omitempty"`
}

// DBStats is the read-only health snapshot for the Maintenance page.
type DBStats struct {
	TotalBytes        int64        `json:"total_bytes"`
	Tables            []TableStat  `json:"tables"`
	ReadingRows       int64        `json:"reading_rows"`
	OldestReading     *string      `json:"oldest_reading"`
	NewestReading     *string      `json:"newest_reading"`
	Chunks            int64        `json:"chunks"`
	CompressedChunks  int64        `json:"compressed_chunks"`
	UncompressedBytes int64        `json:"uncompressed_bytes"`
	CompressedBytes   int64        `json:"compressed_bytes"`
	RetentionPolicy   string       `json:"retention_policy,omitempty"`
	CompressionPolicy string       `json:"compression_policy,omitempty"`
	Sources           []SourceStat `json:"sources"`
}

// DBStats gathers size/row/time-span/chunk info. Every TimescaleDB-specific
// query is best-effort so the call still succeeds on plain Postgres.
func (d *DB) DBStats(ctx context.Context) (DBStats, error) {
	var st DBStats
	_ = d.pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&st.TotalBytes)

	st.Tables = make([]TableStat, 0, len(adminTables))
	for _, t := range adminTables {
		ts := TableStat{Name: t}
		// hypertable_size covers chunks; fall back to plain relation size.
		if err := d.pool.QueryRow(ctx, `SELECT hypertable_size($1)`, t).Scan(&ts.Bytes); err != nil {
			_ = d.pool.QueryRow(ctx, `SELECT pg_total_relation_size($1::regclass)`, t).Scan(&ts.Bytes)
		}
		// exact count — these tables are modest and accuracy matters on this page
		_ = d.pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, pgIdent(t))).Scan(&ts.Rows)
		st.Tables = append(st.Tables, ts)
	}

	_ = d.pool.QueryRow(ctx, `SELECT count(*) FROM readings`).Scan(&st.ReadingRows)
	var oldest, newest *time.Time
	_ = d.pool.QueryRow(ctx, `SELECT min(ts), max(ts) FROM readings`).Scan(&oldest, &newest)
	st.OldestReading, st.NewestReading = iso(oldest), iso(newest)

	// chunk + compression info (TimescaleDB only)
	_ = d.pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE is_compressed)
FROM timescaledb_information.chunks WHERE hypertable_name='readings'`).
		Scan(&st.Chunks, &st.CompressedChunks)
	_ = d.pool.QueryRow(ctx, `
SELECT COALESCE(sum(before_compression_total_bytes),0),
       COALESCE(sum(after_compression_total_bytes),0)
FROM hypertable_compression_stats('readings')`).
		Scan(&st.UncompressedBytes, &st.CompressedBytes)
	_ = d.pool.QueryRow(ctx, `
SELECT config->>'drop_after' FROM timescaledb_information.jobs
WHERE proc_name='policy_retention' AND hypertable_name='readings' LIMIT 1`).Scan(&st.RetentionPolicy)
	_ = d.pool.QueryRow(ctx, `
SELECT config->>'compress_after' FROM timescaledb_information.jobs
WHERE proc_name='policy_compression' AND hypertable_name='readings' LIMIT 1`).Scan(&st.CompressionPolicy)

	// per-source reading volume
	if rows, err := d.pool.Query(ctx, `
SELECT COALESCE(source,'(none)'), count(*), min(ts), max(ts)
FROM readings GROUP BY source ORDER BY count(*) DESC`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var s SourceStat
			var oldest, newest *time.Time
			if err := rows.Scan(&s.Source, &s.Rows, &oldest, &newest); err == nil {
				if o := iso(oldest); o != nil {
					s.Oldest = *o
				}
				if n := iso(newest); n != nil {
					s.Newest = *n
				}
				st.Sources = append(st.Sources, s)
			}
		}
	}
	return st, nil
}

// RunMaintenance executes an idempotent, non-destructive maintenance op. These
// statements (VACUUM, REINDEX, CALL) must not run inside a transaction; pool.Exec
// with no args uses the simple query protocol, which is exactly that.
func (d *DB) RunMaintenance(ctx context.Context, op string) error {
	switch op {
	case "vacuum":
		_, err := d.pool.Exec(ctx, `VACUUM (ANALYZE) readings`)
		return err
	case "reindex":
		for _, t := range []string{"meter_index", "meter_source", "meters", "sdr_devices"} {
			if _, err := d.pool.Exec(ctx, `REINDEX TABLE `+pgIdent(t)); err != nil {
				return err
			}
		}
		return nil
	case "refresh_agg":
		_, err := d.pool.Exec(ctx, `CALL refresh_continuous_aggregate('readings_1m', NULL, NULL)`)
		return err
	case "compress":
		// compress every chunk past the policy horizon that isn't already compressed
		_, err := d.pool.Exec(ctx, `
SELECT compress_chunk(c, if_not_compressed => true)
FROM show_chunks('readings', older_than => INTERVAL '7 days') c`)
		return err
	default:
		return fmt.Errorf("unknown maintenance op %q", op)
	}
}

// DeleteReadingsOlderThan drops whole chunks older than days (cheap), then cleans
// any stragglers in the boundary chunk. Returns rows removed by the DELETE pass.
func (d *DB) DeleteReadingsOlderThan(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, fmt.Errorf("days must be > 0")
	}
	iv := fmt.Sprintf("%d days", days)
	// best-effort whole-chunk drop on both the raw table and the aggregate
	_, _ = d.pool.Exec(ctx, `SELECT drop_chunks('readings', older_than => $1::interval)`, iv)
	_, _ = d.pool.Exec(ctx, `SELECT drop_chunks('readings_1m', older_than => $1::interval)`, iv)
	tag, err := d.pool.Exec(ctx, `DELETE FROM readings WHERE ts < now() - $1::interval`, iv)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteReadingsBySource removes all readings (and coverage rows) from one dongle.
func (d *DB) DeleteReadingsBySource(ctx context.Context, source string) (int64, error) {
	if source == "" {
		return 0, fmt.Errorf("source required")
	}
	tag, err := d.pool.Exec(ctx, `DELETE FROM readings WHERE source=$1`, source)
	if err != nil {
		return 0, err
	}
	_, _ = d.pool.Exec(ctx, `DELETE FROM meter_source WHERE source=$1`, source)
	_, _ = d.pool.Exec(ctx, `DELETE FROM capture_heartbeat WHERE source=$1`, source)
	return tag.RowsAffected(), nil
}

// PurgeAllReadings wipes every reading and the derived registries/aggregate. The
// meter annotations (meters) and settings are preserved.
func (d *DB) PurgeAllReadings(ctx context.Context) error {
	if _, err := d.pool.Exec(ctx, `TRUNCATE readings`); err != nil {
		return err
	}
	// clear derived data; drop all aggregate chunks (best-effort on plain PG)
	_, _ = d.pool.Exec(ctx, `SELECT drop_chunks('readings_1m', older_than => now() + interval '1000 years')`)
	_, _ = d.pool.Exec(ctx, `TRUNCATE meter_index`)
	_, _ = d.pool.Exec(ctx, `TRUNCATE meter_source`)
	return nil
}

// PurgeTests deletes all test-window records (underlying readings stay).
func (d *DB) PurgeTests(ctx context.Context) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM test_windows`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// pgIdent quotes a trusted, internal table identifier for interpolation into
// statements that can't take it as a parameter (VACUUM/REINDEX/count(*)).
func pgIdent(name string) string { return `"` + name + `"` }
