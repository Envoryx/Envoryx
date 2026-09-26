package project

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/logs"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Settings-table keys of the log history.
const (
	SettingLogHistory      = "log_history"
	SettingLogHistoryDays  = "log_history_days"
	SettingLogHistoryMaxMB = "log_history_max_mb"
)

// Defaults and bounds of the log history.
const (
	DefaultLogHistoryDays  = 7
	DefaultLogHistoryMaxMB = 1024
	MaxLogHistoryDays      = 365
	MinLogHistoryMaxMB     = 50
	MaxLogHistoryMaxMB     = 1 << 20 // 1 TB
)

// logPruneInterval is how often retention is applied and finished days are compressed.
const logPruneInterval = time.Hour

// LogHistorySettings is the stored configuration of the log history.
type LogHistorySettings struct {
	// Enabled keeps collecting container output (on by default).
	Enabled bool `json:"enabled"`
	// RetentionDays drops days older than this.
	RetentionDays int `json:"retentionDays"`
	// MaxMB drops the oldest days beyond this size, across all projects.
	MaxMB int `json:"maxMb"`
}

// LogHistoryUpdate changes some of the settings; nil fields stay.
type LogHistoryUpdate struct {
	Enabled       *bool `json:"enabled"`
	RetentionDays *int  `json:"retentionDays"`
	MaxMB         *int  `json:"maxMb"`
}

// LogHistoryInfo is what the settings page shows.
type LogHistoryInfo struct {
	LogHistorySettings
	// Available is false when the history directory could not be opened.
	Available bool       `json:"available"`
	Dir       string     `json:"dir,omitempty"`
	Usage     logs.Usage `json:"usage"`
	// Following counts the containers being read right now.
	Following int `json:"following"`
}

// SetLogStore installs the log history (nil = none, queries go to Docker only).
func (m *Manager) SetLogStore(s *logs.Store) {
	m.logStore = s
	if s == nil {
		m.logCollector = nil
		return
	}
	m.logCollector = &logs.Collector{
		Engine:  m.engine,
		Store:   s,
		Collect: func(c docker.Container) bool { return validate.UUID(c.ProjectID()) == nil && IsLogService(c.Service()) },
		Floor:   func() time.Time { return m.logFloor(context.Background()) },
		Log:     m.log,
	}
}

// IsLogService reports whether a container's service label names one whose output the
// Logs tab shows (not the one-off helpers such as git or template).
func IsLogService(kind string) bool {
	if wid, ok := strings.CutPrefix(kind, "worker:"); ok {
		return validate.UUID(wid) == nil
	}
	if name := store.ServiceKind(kind).DatabaseName(); name != "" {
		return ValidateDatabaseServiceName(name) == nil
	}
	switch store.ServiceKind(kind) {
	case store.ServicePHP, store.ServiceWeb, store.ServiceDatabase, store.ServicePython, store.ServiceGo, store.ServiceNode, store.ServiceRedis, store.ServiceMemcached, store.ServiceMailpit, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceOpenSearchDashboards, store.ServiceOllama, store.ServiceStorage:
		return true
	}
	return false
}

func (m *Manager) intSetting(ctx context.Context, key string, def, lo, hi int) int {
	v, err := m.store.Settings.Get(ctx, key)
	if err != nil {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

// LogHistory returns the log history settings.
func (m *Manager) LogHistory(ctx context.Context) LogHistorySettings {
	v, err := m.store.Settings.Get(ctx, SettingLogHistory)
	return LogHistorySettings{
		Enabled:       err != nil || v != "false",
		RetentionDays: m.intSetting(ctx, SettingLogHistoryDays, DefaultLogHistoryDays, 1, MaxLogHistoryDays),
		MaxMB:         m.intSetting(ctx, SettingLogHistoryMaxMB, DefaultLogHistoryMaxMB, MinLogHistoryMaxMB, MaxLogHistoryMaxMB),
	}
}

// SetLogHistory validates and stores a change of the settings.
func (m *Manager) SetLogHistory(ctx context.Context, upd LogHistoryUpdate) (LogHistorySettings, error) {
	changes := map[string]any{}
	if upd.RetentionDays != nil {
		if *upd.RetentionDays < 1 || *upd.RetentionDays > MaxLogHistoryDays {
			return LogHistorySettings{}, fmt.Errorf("%w: keep the log history for 1 to %d days", validate.ErrInvalid, MaxLogHistoryDays)
		}
		if err := m.store.Settings.Set(ctx, SettingLogHistoryDays, strconv.Itoa(*upd.RetentionDays)); err != nil {
			return LogHistorySettings{}, err
		}
		changes["logHistoryDays"] = *upd.RetentionDays
	}
	if upd.MaxMB != nil {
		if *upd.MaxMB < MinLogHistoryMaxMB || *upd.MaxMB > MaxLogHistoryMaxMB {
			return LogHistorySettings{}, fmt.Errorf("%w: the log history may use %d MB to %d MB", validate.ErrInvalid, MinLogHistoryMaxMB, MaxLogHistoryMaxMB)
		}
		if err := m.store.Settings.Set(ctx, SettingLogHistoryMaxMB, strconv.Itoa(*upd.MaxMB)); err != nil {
			return LogHistorySettings{}, err
		}
		changes["logHistoryMaxMb"] = *upd.MaxMB
	}
	if upd.Enabled != nil {
		if err := m.store.Settings.Set(ctx, SettingLogHistory, strconv.FormatBool(*upd.Enabled)); err != nil {
			return LogHistorySettings{}, err
		}
		changes["logHistory"] = *upd.Enabled
	}
	if len(changes) > 0 {
		m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", changes)
	}
	return m.LogHistory(ctx), nil
}

// LogHistoryInfo returns the settings with the space the history uses.
func (m *Manager) LogHistoryInfo(ctx context.Context) LogHistoryInfo {
	info := LogHistoryInfo{LogHistorySettings: m.LogHistory(ctx)}
	if m.logStore == nil {
		return info
	}
	info.Available = true
	info.Dir = m.logStore.Dir()
	info.Usage, _ = m.logStore.Usage()
	info.Following = m.logCollector.Following()
	return info
}

// ClearLogHistory deletes every stored line. What Docker still holds is not collected
// again.
func (m *Manager) ClearLogHistory(ctx context.Context) error {
	if m.logStore == nil {
		return fmt.Errorf("%w: the log history is not available", ErrNotConfigured)
	}
	before, _ := m.logStore.Usage()
	if err := m.logStore.Clear(time.Now()); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionLogHistoryCleared, "settings", "", map[string]any{"bytes": before.Bytes, "files": before.Files})
	return nil
}

// logFloor is the oldest line worth collecting: nothing retention would drop right away
// and nothing from before the last clear.
func (m *Manager) logFloor(ctx context.Context) time.Time {
	floor := time.Now().AddDate(0, 0, -m.LogHistory(ctx).RetentionDays)
	if c := m.logStore.ClearedAt(); c.After(floor) {
		floor = c
	}
	return floor
}

// RunLogHistory collects container output into the history until ctx ends: every
// interval it picks up containers that started (or stopped with output not yet stored),
// every hour it applies retention and compresses finished days.
func (m *Manager) RunLogHistory(ctx context.Context, interval time.Duration, log *slog.Logger) {
	if m.logCollector == nil {
		return
	}
	defer m.logCollector.StopAll()
	t := time.NewTicker(interval)
	defer t.Stop()
	var nextPrune time.Time
	for {
		s := m.LogHistory(ctx)
		m.logCollector.Pass(ctx, s.Enabled)
		if now := time.Now(); !now.Before(nextPrune) {
			m.pruneLogHistory(ctx, s, log)
			nextPrune = now.Add(logPruneInterval)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Manager) pruneLogHistory(ctx context.Context, s LogHistorySettings, log *slog.Logger) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		log.Warn("log history: list projects", "err", err)
		return
	}
	known := map[string]bool{}
	for _, p := range projects {
		known[p.ID] = true
	}
	res, err := m.logStore.Prune(time.Now(), s.RetentionDays, int64(s.MaxMB)<<20, func(id string) bool { return known[id] })
	if err != nil {
		log.Warn("log history: prune", "err", err)
		return
	}
	if res.Removed > 0 || res.Compressed > 0 {
		log.Info("log history pruned", "compressed", res.Compressed, "removed", res.Removed, "freedBytes", res.Freed)
	}
}

// historyKey returns the history of a service when the history is on and holds lines
// for it; otherwise queries go to the container.
func (m *Manager) historyKey(ctx context.Context, id string, kind store.ServiceKind) (logs.Key, bool, error) {
	if m.logStore == nil || !m.LogHistory(ctx).Enabled {
		return logs.Key{}, false, nil
	}
	if err := validate.UUID(id); err != nil {
		return logs.Key{}, false, ErrNotFound
	}
	if _, err := m.store.Projects.Get(ctx, id); err != nil {
		return logs.Key{}, false, err
	}
	k := logs.Key{Project: id, Service: string(kind)}
	return k, m.logStore.Has(k), nil
}
