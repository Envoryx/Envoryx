package sshd

import (
	"testing"
	"time"
)

func TestFailLimiter(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	l := newFailLimiter()
	l.now = func() time.Time { return now }
	failN := func(ip, user string, n int) *lockout {
		var lo *lockout
		for range n {
			if got := l.fail(ip, user); got != nil {
				lo = got
			}
		}
		return lo
	}

	// Failures spread wider than the window never add up.
	for range 5 {
		if failN("10.0.0.1", "shop", userFailLimit-1) != nil {
			t.Fatal("locked below the limit")
		}
		now = now.Add(failWindow + time.Second)
	}
	if !l.lockedUntil("10.0.0.1", "shop").IsZero() {
		t.Fatal("old failures must expire")
	}

	// The limit within the window locks this address and user, nobody else.
	lo := failN("10.0.0.1", "shop", userFailLimit)
	if lo == nil || lo.perAddress || lo.failures != userFailLimit || !lo.until.Equal(now.Add(lockDuration)) {
		t.Fatalf("lockout: %+v", lo)
	}
	if l.lockedUntil("10.0.0.1", "shop").IsZero() {
		t.Fatal("address and user must be locked")
	}
	if !l.lockedUntil("10.0.0.1", "blog").IsZero() || !l.lockedUntil("10.0.0.2", "shop").IsZero() {
		t.Fatal("other users and addresses must not be locked")
	}
	now = now.Add(lockDuration + time.Second)
	if !l.lockedUntil("10.0.0.1", "shop").IsZero() {
		t.Fatal("the lockout must end")
	}
	// After the lockout a single failure does not lock again.
	if l.fail("10.0.0.1", "shop") != nil {
		t.Fatal("relocked at once")
	}

	// A success clears the user's counter.
	failN("10.0.0.3", "shop", userFailLimit-1)
	l.reset("10.0.0.3", "shop")
	if failN("10.0.0.3", "shop", userFailLimit-1) != nil {
		t.Fatal("reset must clear the counter")
	}

	// Guessing across many project names trips the per-address limit.
	now = now.Add(time.Hour)
	var addr *lockout
	for i := range addrFailLimit {
		if lo := l.fail("10.0.0.4", string(rune('a'+i%26))+"x"); lo != nil {
			addr = lo
		}
	}
	if addr == nil || !addr.perAddress {
		t.Fatalf("per-address lockout: %+v", addr)
	}
	if l.lockedUntil("10.0.0.4", "anything").IsZero() {
		t.Fatal("the whole address must be locked")
	}
}
