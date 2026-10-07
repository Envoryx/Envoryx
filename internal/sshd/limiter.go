package sshd

import (
	"sync"
	"time"
)

// Login throttling, modelled on fail2ban's sshd jail (5 failures in 10 minutes ban for
// 10 minutes) but looser, because one person behind a NAT or Docker's port mapping shares
// the address with the whole team:
//
//   - Only guesses count: a wrong or unknown API token. Public keys never count (clients
//     offer every key in their agent, and keys cannot be guessed), neither does an empty
//     password (OpenSSH without a prompt sends one) nor a valid credential that is refused
//     for the project (unknown slug, no role): that is a permission problem, not brute force.
//   - Failures expire: a counter starts with the first failure and is forgotten failWindow
//     later, so a few typos spread over a day never add up.
//   - The lockout is per address and SSH user (= project), so a colleague on the same
//     address working on another project is not affected. Spraying many project names from
//     one address still trips the per-address limit.
//   - While locked, every login for that address and user is refused, correct ones
//     included: otherwise a guesser would simply keep going and learn when it hit.
//     A successful login clears the counter of its address and user.
const (
	failWindow    = 10 * time.Minute
	lockDuration  = 5 * time.Minute
	userFailLimit = 10 // per address and SSH user within failWindow
	addrFailLimit = 30 // per address over all SSH users within failWindow
	// pruneAbove is the table size at which expired entries are dropped, so a scan from
	// many addresses cannot grow it without bound.
	pruneAbove = 1024
)

type failEntry struct {
	n     int
	since time.Time // first failure of the current window
	until time.Time // locked until, zero when not locked
}

// failLimiter counts login failures per address and per address+user.
type failLimiter struct {
	mu    sync.Mutex
	now   func() time.Time
	fails map[string]*failEntry
}

func newFailLimiter() *failLimiter {
	return &failLimiter{now: time.Now, fails: map[string]*failEntry{}}
}

func userKey(ip, user string) string { return ip + "\x00" + user }

// lockedUntil reports until when logins for ip and user are refused (zero time when they
// are not).
func (l *failLimiter) lockedUntil(ip, user string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var until time.Time
	for _, k := range []string{ip, userKey(ip, user)} {
		if e := l.fails[k]; e != nil && now.Before(e.until) && e.until.After(until) {
			until = e.until
		}
	}
	return until
}

// lockout describes a lockout that a failure just started.
type lockout struct {
	perAddress bool // the whole address, not just this user
	failures   int
	until      time.Time
}

// fail records a failed guess and reports the lockout it started, if any.
func (l *failLimiter) fail(ip, user string) *lockout {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.fails) > pruneAbove {
		for k, e := range l.fails {
			if now.After(e.until) && now.Sub(e.since) > failWindow {
				delete(l.fails, k)
			}
		}
	}
	var started *lockout
	for _, c := range []struct {
		key        string
		limit      int
		perAddress bool
	}{{userKey(ip, user), userFailLimit, false}, {ip, addrFailLimit, true}} {
		e := l.fails[c.key]
		if e == nil || now.Sub(e.since) > failWindow {
			e = &failEntry{since: now, until: timeOrZero(e)}
			l.fails[c.key] = e
		}
		e.n++
		if e.n >= c.limit && !now.Before(e.until) {
			e.until = now.Add(lockDuration)
			if started == nil || c.perAddress {
				started = &lockout{perAddress: c.perAddress, failures: e.n, until: e.until}
			}
			// A new window starts after the lockout, so the next guess does not lock again
			// at once.
			e.n, e.since = 0, e.until
		}
	}
	return started
}

// timeOrZero keeps a running lockout when a window is restarted.
func timeOrZero(e *failEntry) time.Time {
	if e == nil {
		return time.Time{}
	}
	return e.until
}

// reset forgets the failures of ip and user after a successful login. The per-address
// counter stays and expires on its own: one good account must not clear the way for
// guesses at the others.
func (l *failLimiter) reset(ip, user string) {
	l.mu.Lock()
	delete(l.fails, userKey(ip, user))
	l.mu.Unlock()
}
