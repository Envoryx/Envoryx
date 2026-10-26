// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import "io"

// ansiStripper drops terminal escape sequences (colours, cursor moves, window titles)
// from what it passes on. A sequence may be split over two writes, so it keeps its
// state between them.
type ansiStripper struct {
	w io.Writer
	// state is 0 in text, 'e' after ESC, '[' in a CSI sequence, ']' in an OSC one, '\\'
	// after ESC within OSC and '(' before a charset's letter.
	state byte
}

func (s *ansiStripper) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch s.state {
		case 0:
			if b == 0x1b {
				s.state = 'e'
			} else {
				out = append(out, b)
			}
		case 'e':
			switch b {
			case '[', ']', '(':
				s.state = b
			case ')':
				s.state = '('
			default:
				s.state = 0 // a two-byte sequence such as ESC =
			}
		case '[':
			if b >= 0x40 && b <= 0x7e {
				s.state = 0
			}
		case ']':
			switch b {
			case 0x07:
				s.state = 0
			case 0x1b:
				s.state = '\\'
			}
		default: // the byte that ends an OSC after ESC, or the charset's letter
			s.state = 0
		}
	}
	if _, err := s.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
