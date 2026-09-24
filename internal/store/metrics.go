package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Metric resolutions in seconds.
const (
	MetricRes1m = 60
	MetricRes5m = 300
	MetricRes1h = 3600
)

// MetricSample is one container's usage in one time bucket.
type MetricSample struct {
	ProjectID string
	Container string
	Res       int
	TS        int64 // bucket start, unix seconds
	CPU       float64
	CPUMax    float64
	Mem       int64
	MemMax    int64
	NetRx     float64
	NetTx     float64
	BlkRead   float64
	BlkWrite  float64
	Samples   int
}

// MetricSize is the disk space of one thing of a project at one time.
type MetricSize struct {
	ProjectID string
	Kind      string // volume | files | backups
	Name      string
	TS        int64
	Bytes     int64
}

// Metrics is the repository for the resource history.
type Metrics struct{ db *sql.DB }

// AddSamples stores one-minute samples (replacing a sample of the same bucket).
func (r *Metrics) AddSamples(ctx context.Context, samples []MetricSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO metric_samples (project_id, container, res, ts, cpu, cpu_max, mem, mem_max, net_rx, net_tx, blk_read, blk_write, samples)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range samples {
		if _, err := stmt.ExecContext(ctx, s.ProjectID, s.Container, s.Res, s.TS, s.CPU, s.CPUMax, s.Mem, s.MemMax, s.NetRx, s.NetTx, s.BlkRead, s.BlkWrite, s.Samples); err != nil {
			return fmt.Errorf("insert metric sample: %w", err)
		}
	}
	return tx.Commit()
}

// Rollup aggregates the samples of resolution from into buckets of resolution to for
// the buckets that start in [since, until). Averages are weighted by the number of
// minute samples, maxima kept; running it again for the same span replaces the result.
func (r *Metrics) Rollup(ctx context.Context, from, to int, since, until int64) error {
	_, err := r.db.ExecContext(ctx, `INSERT OR REPLACE INTO metric_samples (project_id, container, res, ts, cpu, cpu_max, mem, mem_max, net_rx, net_tx, blk_read, blk_write, samples)
		SELECT project_id, container, ?, (ts / ?) * ?,
			SUM(cpu * samples) / SUM(samples), MAX(cpu_max),
			CAST(SUM(mem * samples) / SUM(samples) AS INTEGER), MAX(mem_max),
			SUM(net_rx * samples) / SUM(samples), SUM(net_tx * samples) / SUM(samples),
			SUM(blk_read * samples) / SUM(samples), SUM(blk_write * samples) / SUM(samples),
			SUM(samples)
		FROM metric_samples
		WHERE res = ? AND ts >= ? AND ts < ?
		GROUP BY project_id, container, ts / ?`,
		to, to, to, from, since, until, to)
	if err != nil {
		return fmt.Errorf("roll up metrics: %w", err)
	}
	return nil
}

// Prune removes samples of a resolution older than before (unix seconds).
func (r *Metrics) Prune(ctx context.Context, res int, before int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM metric_samples WHERE res = ? AND ts < ?`, res, before)
	return err
}

// PruneSizes removes size measurements older than before.
func (r *Metrics) PruneSizes(ctx context.Context, before int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM metric_sizes WHERE ts < ?`, before)
	return err
}

// Samples returns a project's samples of one resolution in [since, until), by time.
func (r *Metrics) Samples(ctx context.Context, projectID string, res int, since, until int64) ([]MetricSample, error) {
	return r.samples(ctx, `WHERE project_id = ? AND res = ? AND ts >= ? AND ts < ? ORDER BY ts, container`, projectID, res, since, until)
}

// AllSamples returns every project's samples of one resolution in [since, until).
func (r *Metrics) AllSamples(ctx context.Context, res int, since, until int64) ([]MetricSample, error) {
	return r.samples(ctx, `WHERE res = ? AND ts >= ? AND ts < ? ORDER BY ts, project_id, container`, res, since, until)
}

func (r *Metrics) samples(ctx context.Context, where string, args ...any) ([]MetricSample, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT project_id, container, res, ts, cpu, cpu_max, mem, mem_max, net_rx, net_tx, blk_read, blk_write, samples FROM metric_samples `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("select metric samples: %w", err)
	}
	defer rows.Close()
	out := []MetricSample{}
	for rows.Next() {
		var s MetricSample
		if err := rows.Scan(&s.ProjectID, &s.Container, &s.Res, &s.TS, &s.CPU, &s.CPUMax, &s.Mem, &s.MemMax, &s.NetRx, &s.NetTx, &s.BlkRead, &s.BlkWrite, &s.Samples); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AddSizes stores size measurements.
func (r *Metrics) AddSizes(ctx context.Context, sizes []MetricSize) error {
	if len(sizes) == 0 {
		return nil
	}
	var b strings.Builder
	args := make([]any, 0, len(sizes)*5)
	b.WriteString(`INSERT OR REPLACE INTO metric_sizes (project_id, kind, name, ts, bytes) VALUES `)
	for i, s := range sizes {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?, ?, ?, ?)")
		args = append(args, s.ProjectID, s.Kind, s.Name, s.TS, s.Bytes)
	}
	_, err := r.db.ExecContext(ctx, b.String(), args...)
	return err
}

// Sizes returns size measurements in [since, until): one project's, or every project's
// when projectID is empty.
func (r *Metrics) Sizes(ctx context.Context, projectID string, since, until int64) ([]MetricSize, error) {
	q := `SELECT project_id, kind, name, ts, bytes FROM metric_sizes WHERE ts >= ? AND ts < ?`
	args := []any{since, until}
	if projectID != "" {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	rows, err := r.db.QueryContext(ctx, q+` ORDER BY ts, kind, name`, args...)
	if err != nil {
		return nil, fmt.Errorf("select metric sizes: %w", err)
	}
	defer rows.Close()
	out := []MetricSize{}
	for rows.Next() {
		var s MetricSize
		if err := rows.Scan(&s.ProjectID, &s.Kind, &s.Name, &s.TS, &s.Bytes); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SizedProjects returns the projects that have size measurements at ts.
func (r *Metrics) SizedProjects(ctx context.Context, ts int64) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT project_id FROM metric_sizes WHERE ts = ?`, ts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DeleteProject forgets a deleted project's history.
func (r *Metrics) DeleteProject(ctx context.Context, projectID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM metric_samples WHERE project_id = ?`, projectID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM metric_sizes WHERE project_id = ?`, projectID)
	return err
}

// Usage reports how many rows the history holds (for the settings page).
func (r *Metrics) Usage(ctx context.Context) (samples, sizes int64, err error) {
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_samples`).Scan(&samples); err != nil {
		return
	}
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_sizes`).Scan(&sizes)
	return
}

// Clear deletes the whole history.
func (r *Metrics) Clear(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM metric_samples`); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM metric_sizes`)
	return err
}
