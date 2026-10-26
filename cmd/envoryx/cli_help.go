// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"flag"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"text/tabwriter"
)

// helpError is an explicit --help given to one command or command group: its own entries
// from the usage text and its flags, not the whole usage.
type helpError struct {
	// cmd is the command as the usage text names it ("project logs", "backup"); "envoryx"
	// or "" is the whole CLI.
	cmd string
	// flags are the command's flags, nil for a group.
	flags *flag.FlagSet
}

func (e *helpError) Error() string { return "help" }
func (e *helpError) Is(target error) bool {
	return target == errCLIHelp
}

// groupHelp is the help of a command group such as "envoryx backup --help".
func groupHelp(group string) error {
	return &helpError{cmd: group}
}

// globalFlags are defined on every flag set and described once, in the usage text.
var globalFlags = []string{"url", "token", "ca-cert", "insecure", "json", "timeout"}

// serverUsage is the help of the commands that run Envoryx itself rather than talk to it.
const serverUsage = `Usage: envoryx serve | healthcheck | version | admin <command>

  serve        run the Envoryx server (what the container starts)
  healthcheck  probe the local server, for the Docker HEALTHCHECK; exits 0 when it
               answers, 1 otherwise
  version      print the version
  admin …      recover access and manage API tokens on the server ("admin --help")

"envoryx help" lists the commands that work with a running Envoryx.
`

// commandHelp is the help of one command: the lines the usage text has for it, its own
// flags, and a pointer to the global ones. A command the usage text does not list gets
// the whole text.
func commandHelp(cmd string, fs *flag.FlagSet) string {
	words := strings.Fields(cmd)
	if len(words) == 0 || words[0] == "envoryx" {
		return cliUsage
	}
	var entries strings.Builder
	in := false
	for _, line := range strings.Split(cliUsage, "\n") {
		switch {
		case strings.HasPrefix(line, "    "):
			// A continuation of the entry above, indented to its description column.
			if in {
				entries.WriteString("        " + line + "\n")
			}
		case strings.HasPrefix(line, "  ") && usageEntryFor(strings.Fields(line), words):
			in = true
			entries.WriteString("  envoryx " + line[2:] + "\n")
		default:
			in = false
		}
	}
	if entries.Len() == 0 {
		return cliUsage
	}
	var b strings.Builder
	b.WriteString("Usage:\n")
	b.WriteString(entries.String())
	if own := ownFlags(fs); own != "" {
		b.WriteString("\nFlags:\n" + own)
	}
	if words[0] == "login" {
		// login has no flags of its own: the global ones are what it stores.
		b.WriteString("\n" + usageSection("Global flags:"))
	} else {
		b.WriteString("\nGlobal flags: --url, --token, --ca-cert, --insecure, --json, --timeout (\"envoryx help\" describes them).\n")
	}
	return b.String()
}

// usageEntryFor tells whether a usage line ("project start|stop|restart <project>")
// describes the command (["project", "stop"]); a group (["project"]) has every entry
// that starts with its name.
func usageEntryFor(fields, words []string) bool {
	for i, w := range words {
		if i >= len(fields) || !slices.Contains(strings.Split(fields[i], "|"), w) {
			return false
		}
	}
	return true
}

// usageSection is one section of the usage text, from its heading to the next blank line.
func usageSection(heading string) string {
	_, rest, ok := strings.Cut(cliUsage, "\n"+heading+"\n")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "\n\n")
	return heading + "\n" + body + "\n"
}

// ownFlags lists a command's flags without the global ones, a short and a long name for
// the same setting (-o, --output) on one line.
func ownFlags(fs *flag.FlagSet) string {
	if fs == nil {
		return ""
	}
	type entry struct {
		names []string
		flag  *flag.Flag
	}
	var entries []*entry
	byTarget := map[uintptr]*entry{}
	fs.VisitAll(func(f *flag.Flag) {
		if slices.Contains(globalFlags, f.Name) {
			return
		}
		// Two names bound to the same variable are one flag.
		v := reflect.ValueOf(f.Value)
		if v.Kind() == reflect.Pointer {
			if e, ok := byTarget[v.Pointer()]; ok {
				e.names = append(e.names, f.Name)
				return
			}
		}
		e := &entry{names: []string{f.Name}, flag: f}
		if v.Kind() == reflect.Pointer {
			byTarget[v.Pointer()] = e
		}
		entries = append(entries, e)
	})
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, e := range entries {
		// The longer name explains itself best, so its usage text is the one shown.
		slices.SortFunc(e.names, func(a, b string) int { return len(a) - len(b) })
		long := fs.Lookup(e.names[len(e.names)-1])
		var names []string
		for _, n := range e.names {
			names = append(names, flagName(n))
		}
		kind, usage := flag.UnquoteUsage(long)
		switch kind {
		case "", "value":
			kind = ""
			if _, isBool := long.Value.(interface{ IsBoolFlag() bool }); !isBool {
				kind = " VALUE"
			}
		case "string":
			kind = " VALUE"
		case "int", "uint":
			kind = " N"
		default:
			kind = " " + strings.ToUpper(kind)
		}
		if d := long.DefValue; d != "" && d != "false" && d != "0" && d != "0s" && d != "[]" {
			usage += fmt.Sprintf(" (default %s)", d)
		}
		fmt.Fprintf(tw, "  %s%s\t%s\n", strings.Join(names, ", "), kind, usage)
	}
	_ = tw.Flush()
	return b.String()
}
