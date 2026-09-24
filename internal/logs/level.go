// Package logs holds what Envoryx does with container output beyond streaming it: the
// severity heuristic, filtering, the error-frequency statistics and the persistent
// history that outlives the containers.
package logs

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Level is the severity Envoryx reads from a line. Containers do not report one, so it
// is a heuristic over the text: stderr alone says little (PHP-FPM and nginx write their
// start-up notices there, many apps log errors to stdout).
type Level int

const (
	LevelInfo Level = iota
	LevelWarn
	LevelError
)

// String returns "", "warn" or "error" – the value used in the API.
func (l Level) String() string {
	switch l {
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return ""
}

// ParseLevel reads a minimum level from the API ("", "all", "warn", "error").
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all", "info":
		return LevelInfo, true
	case "warn", "warning", "warnings":
		return LevelWarn, true
	case "error", "errors":
		return LevelError, true
	}
	return LevelInfo, false
}

var (
	// Whole words only: "0 errors", "error_log" or "ErrorDocument" do not count, and
	// neither does Go's "err=<nil>".
	errorWords = regexp.MustCompile(`(?i)\b(fatal|panic|emerg|emergency|crit|critical|error|exception|traceback)\b`)
	warnWords  = regexp.MustCompile(`(?i)\b(warn|warning|deprecated)\b`)
	// Access log lines: `"GET / HTTP/1.1" 502 …` – a server error is an error.
	status5xx = regexp.MustCompile(`" 5\d\d `)
	// logfmt: `level=error msg=…`.
	logfmtLevel = regexp.MustCompile(`(?i)\b(?:level|lvl|severity)="?(\w+)`)
)

// Classify returns the level of one line. Structured (JSON) lines are judged by their
// level field alone, so a message that merely mentions an error is not one.
func Classify(text string) Level {
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") {
		if l, ok := jsonLevel(t); ok {
			return l
		}
	}
	if m := logfmtLevel.FindStringSubmatch(text); m != nil {
		return wordLevel(m[1])
	}
	switch {
	case errorWords.MatchString(text) || status5xx.MatchString(text):
		return LevelError
	case warnWords.MatchString(text):
		return LevelWarn
	}
	return LevelInfo
}

func jsonLevel(text string) (Level, bool) {
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) != nil {
		return 0, false
	}
	for _, k := range []string{"level", "severity", "lvl", "log.level", "levelname", "loglevel"} {
		switch v := m[k].(type) {
		case string:
			return wordLevel(v), true
		case float64:
			// pino/bunyan: 40 warn, 50 error, 60 fatal.
			switch {
			case v >= 50:
				return LevelError, true
			case v >= 40:
				return LevelWarn, true
			}
			return LevelInfo, true
		}
	}
	return 0, false
}

func wordLevel(s string) Level {
	switch s = strings.ToLower(s); {
	case s == "err" || errorWords.MatchString(s):
		return LevelError
	case warnWords.MatchString(s):
		return LevelWarn
	}
	return LevelInfo
}
