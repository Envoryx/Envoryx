package sshd

import "time"

// SetLoginGrace shortens the authentication window for tests.
func (s *Server) SetLoginGrace(d time.Duration) { s.loginGrace.Store(int64(d)) }
