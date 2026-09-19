// Package disk reports free space on the directories Envoryx writes to and refuses
// operations that would fill them up.
package disk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/envoryx/envoryx/internal/notify"
)

// Usage is the state of the filesystem behind a directory.
type Usage struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
	// Low is set when free space is below the warning threshold.
	Low bool `json:"low"`
	// fsid identifies the filesystem so that several paths on one disk are listed once.
	fsid string
}

const (
	// LowFreeBytes and LowFreePercent define "running out": below either, the dashboard
	// warns and a notification is sent.
	LowFreeBytes   = 2 << 30 // 2 GiB
	LowFreePercent = 5.0
	// reserveBytes is what an operation must leave free besides its own needs.
	reserveBytes = 512 << 20
)

// ErrInsufficient is returned by Require when an operation does not fit.
var ErrInsufficient = errors.New("insufficient disk space")

// Check returns the usage of each directory, de-duplicated by filesystem (the first
// path wins). Directories that cannot be inspected are skipped.
func Check(paths ...string) []Usage {
	var out []Usage
	seen := map[string]bool{}
	for _, p := range paths {
		u, err := usage(p)
		if err != nil {
			continue
		}
		if seen[u.fsid] {
			continue
		}
		seen[u.fsid] = true
		out = append(out, u)
	}
	return out
}

func isLow(u Usage) bool {
	if u.TotalBytes == 0 {
		return false
	}
	pct := float64(u.FreeBytes) / float64(u.TotalBytes) * 100
	return u.FreeBytes < LowFreeBytes || pct < LowFreePercent
}

// Require fails when dir does not have need bytes plus a reserve available.
func Require(dir string, need uint64) error {
	u, err := usage(dir)
	if err != nil {
		return nil // an unknown filesystem is no reason to block; the write itself will fail loudly
	}
	if u.FreeBytes < need+reserveBytes {
		return fmt.Errorf("%w: %s has %s free, the operation needs about %s plus a %s reserve", ErrInsufficient, dir, Human(u.FreeBytes), Human(need), Human(reserveBytes))
	}
	return nil
}

// Human formats bytes for messages.
func Human(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Monitor checks the directories periodically and notifies when one runs low (once,
// until it recovers). It returns when ctx ends.
func Monitor(ctx context.Context, interval time.Duration, notifier *notify.Service, log *slog.Logger, paths ...string) {
	low := map[string]bool{}
	tick := func() {
		for _, u := range Check(paths...) {
			switch {
			case u.Low && !low[u.Path]:
				low[u.Path] = true
				log.Warn("disk space low", "path", u.Path, "free", Human(u.FreeBytes), "total", Human(u.TotalBytes))
				if notifier != nil {
					notifier.Notify(ctx, notify.Event{Kind: "storage.low", Level: notify.Warning, Key: "storage.low|" + u.Path,
						Title: "Disk space low: " + u.Path, Message: fmt.Sprintf("%s of %s free. Backups, image pulls and the database need room; delete old backups or unused images.", Human(u.FreeBytes), Human(u.TotalBytes))})
				}
			case !u.Low && low[u.Path]:
				delete(low, u.Path)
				log.Info("disk space recovered", "path", u.Path, "free", Human(u.FreeBytes))
				if notifier != nil {
					notifier.Clear("storage.low|" + u.Path)
					notifier.Notify(ctx, notify.Event{Kind: "storage.low", Level: notify.Info, Key: "storage.recovered|" + u.Path,
						Title: "Disk space recovered: " + u.Path, Message: fmt.Sprintf("%s of %s free again.", Human(u.FreeBytes), Human(u.TotalBytes))})
				}
			}
		}
	}
	tick()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
