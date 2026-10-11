package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// SuspensionFile is the hoster's suspension in the config directory; without it the
// instance is not suspended.
const SuspensionFile = "suspension.json"

// MaxSuspensionMessage is the longest text a hoster can leave for the customer.
const MaxSuspensionMessage = 2000

// Suspension is a hoster's lock on a managed instance (an unpaid invoice, abuse): its
// projects are stopped, and its users see nothing but the message until the hoster
// lifts it. The data stays as it is, and lifting it starts nothing again.
type Suspension struct {
	Since time.Time `json:"since"`
	// Message is the hoster's text for the customer; the UI shows a default without it.
	Message string `json:"message,omitempty"`
}

// ErrSuspended is returned for whatever a suspended instance refuses.
var ErrSuspended = errors.New("this instance is suspended by its hoster")

// ParseSuspension reads and checks a suspension.
func ParseSuspension(b []byte) (*Suspension, error) {
	var s Suspension
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("suspension: %w", err)
	}
	if s.Since.IsZero() {
		return nil, errors.New("suspension: since is missing")
	}
	if utf8.RuneCountInString(s.Message) > MaxSuspensionMessage {
		return nil, fmt.Errorf("suspension: the message is longer than %d characters", MaxSuspensionMessage)
	}
	return &s, nil
}

// Err is the ErrSuspended a suspended instance answers with.
func (s *Suspension) Err() error {
	if s == nil {
		return nil
	}
	return ErrSuspended
}
