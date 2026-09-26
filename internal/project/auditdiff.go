package project

import (
	"context"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/manifest"
)

// AuditChange is one setting a project update changed, as the audit log shows it: the
// manifest section (and item: a database, variable, worker or cron job), and the values
// before and after. Secret values never appear – the export only names secrets.
type AuditChange struct {
	Section string `json:"section"`
	Item    string `json:"item,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

// auditState is a project's settings as its manifest, for comparing before and after an
// update. ok is false when the project cannot be read; the entry then goes without a diff.
func (m *Manager) auditState(ctx context.Context, id string) (manifest.Manifest, bool) {
	p, domains, jobs, err := m.manifestState(ctx, id)
	if err != nil {
		return manifest.Manifest{}, false
	}
	return exportState(p, domains, jobs), true
}

// manifestDiff lists what differs between two manifests of the same project, section by
// section in the file's order; databases, variables, workers and cron jobs item by item.
func manifestDiff(before, after manifest.Manifest) []AuditChange {
	var out []AuditChange
	bv, av := reflect.ValueOf(before), reflect.ValueOf(after)
	t := bv.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		section, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if section == "" || section == "-" || section == "version" {
			continue
		}
		b, a := bv.Field(i).Interface(), av.Field(i).Interface()
		if reflect.DeepEqual(b, a) {
			continue
		}
		switch section {
		case "databases":
			out = append(out, mapDiff(section, before.Databases, after.Databases)...)
		case "env":
			out = append(out, mapDiff(section, before.Env, after.Env)...)
		case "workers":
			out = append(out, listDiff(section, before.Workers, after.Workers, func(w manifest.Worker) string { return w.Name })...)
		case "cron":
			out = append(out, listDiff(section, before.Cron, after.Cron, func(c manifest.CronJob) string { return c.Name })...)
		case "domains", "secrets":
			out = append(out, setDiff(section, bv.Field(i).Interface().([]string), av.Field(i).Interface().([]string))...)
		default:
			out = append(out, valueDiff(section, "", bv.Field(i), av.Field(i)))
		}
	}
	return out
}

// valueDiff describes one changed value: added, removed or the keys that differ.
func valueDiff(section, item string, b, a reflect.Value) AuditChange {
	c := AuditChange{Section: section, Item: item}
	switch {
	case isEmpty(b):
		c.To = describeValue(a)
	case isEmpty(a):
		c.From = describeValue(b)
	case b.Kind() == reflect.String || b.Kind() == reflect.Int || b.Kind() == reflect.Bool:
		c.From, c.To = describeValue(b), describeValue(a)
	default:
		c.From, c.To = diffSections(b.Interface(), a.Interface())
	}
	return c
}

// describeValue renders a value the way the change list shows it; a section without
// settings of its own ("redis: true") reads as "on".
func describeValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String, reflect.Int, reflect.Bool:
		return fmtScalar(v)
	}
	if d := describe(v.Interface()); d != "" {
		return d
	}
	return "on"
}

func fmtScalar(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		if v.Bool() {
			return "true"
		}
		return "false"
	default:
		return strconv.FormatInt(v.Int(), 10)
	}
}

func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return v.IsNil() || (v.Kind() != reflect.Pointer && v.Len() == 0)
	}
	return v.IsZero()
}

func mapDiff[V any](section string, before, after map[string]V) []AuditChange {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	var out []AuditChange
	for _, k := range sortedKeys(keys) {
		b, inB := before[k]
		a, inA := after[k]
		if inB && inA && reflect.DeepEqual(b, a) {
			continue
		}
		bv, av := reflect.ValueOf(b), reflect.ValueOf(a)
		if !inB {
			bv = reflect.Zero(bv.Type())
		}
		if !inA {
			av = reflect.Zero(av.Type())
		}
		out = append(out, valueDiff(section, k, bv, av))
	}
	return out
}

func listDiff[T any](section string, before, after []T, name func(T) string) []AuditChange {
	b, a := map[string]T{}, map[string]T{}
	for _, x := range before {
		b[name(x)] = x
	}
	for _, x := range after {
		a[name(x)] = x
	}
	return mapDiff(section, b, a)
}

func setDiff(section string, before, after []string) []AuditChange {
	var out []AuditChange
	for _, x := range before {
		if !slices.Contains(after, x) {
			out = append(out, AuditChange{Section: section, Item: x, From: x})
		}
	}
	for _, x := range after {
		if !slices.Contains(before, x) {
			out = append(out, AuditChange{Section: section, Item: x, To: x})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Item < out[j].Item })
	return out
}
