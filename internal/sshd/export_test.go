package sshd

import "time"

// SetLoginGrace shortens the authentication window for tests.
func (s *Server) SetLoginGrace(d time.Duration) { s.loginGrace.Store(int64(d)) }

// SetRecheck shortens how often open connections' credentials are checked, for tests.
func (s *Server) SetRecheck(d time.Duration) { s.recheck.Store(int64(d)) }
