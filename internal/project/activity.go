package project

import (
	"time"

	"github.com/envoryx/envoryx/internal/notify"
)

// Activity is one thing Envoryx did on its own – without a user asking for it – since the
// process started: projects resumed after a restart, orphaned resources removed. The
// dashboard shows the list so nothing happens behind the user's back; the audit log is
// the durable record.
type Activity struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"` // projects.resumed | docker.orphans_removed
	Items []string  `json:"items"`
}

const (
	ActivityProjectsResumed = "projects.resumed"
	ActivityOrphansRemoved  = "docker.orphans_removed"
	maxActivity             = 20
)

// recordActivity remembers an autonomous action (newest first, bounded) and notifies.
func (m *Manager) recordActivity(kind string, items []string, title, message string) {
	m.reportMu.Lock()
	m.activity = append([]Activity{{At: time.Now().UTC(), Kind: kind, Items: items}}, m.activity...)
	if len(m.activity) > maxActivity {
		m.activity = m.activity[:maxActivity]
	}
	m.reportMu.Unlock()
	if m.notifier != nil {
		m.notifier.Notify(m.ops.root, notify.Event{Kind: kind, Level: notify.Info, Title: title, Message: message, Key: kind + "|" + time.Now().UTC().Format(time.RFC3339Nano)})
	}
}

// Activity returns what Envoryx did on its own since it started, newest first.
func (m *Manager) Activity() []Activity {
	m.reportMu.RLock()
	defer m.reportMu.RUnlock()
	out := make([]Activity, len(m.activity))
	copy(out, m.activity)
	return out
}
