// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

// Command licenses writes the licence texts of the third-party code the envoryx binary
// contains: the Go modules it is built from (go list, for the GOOS/GOARCH of the build), the
// Go standard library, and the npm packages of the web interface, which the web build lists
// in web/dist/third-party-licenses-web.txt. The result goes next to it into web/dist, so it
// is embedded and served as /third-party-licenses.txt.
//
//	go run ./scripts/licenses [-goos linux -goarch arm64]
//
// -goos and -goarch name the build's target; they only go to go list, so the command
// itself still runs on the build machine.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// noticeFile matches the files of a module that carry its licence or attribution notices.
var noticeFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice)([._-].*)?$`)

var rule = strings.Repeat("=", 80)

// targetEnv sets the build's target for go list.
var targetEnv = os.Environ()

func main() {
	pkg := flag.String("pkg", "./cmd/envoryx", "the main package")
	web := flag.String("web", "web/dist/third-party-licenses-web.txt", "the web build's list")
	out := flag.String("o", "web/dist/third-party-licenses.txt", "the file to write")
	goos := flag.String("goos", "", "target GOOS (default: the build machine's)")
	goarch := flag.String("goarch", "", "target GOARCH")
	flag.Parse()
	if *goos != "" {
		targetEnv = append(targetEnv, "GOOS="+*goos)
	}
	if *goarch != "" {
		targetEnv = append(targetEnv, "GOARCH="+*goarch)
	}
	if err := run(*pkg, *web, *out); err != nil {
		fmt.Fprintln(os.Stderr, "licenses:", err)
		os.Exit(1)
	}
}

type module struct{ path, version, dir string }

func run(pkg, webList, out string) error {
	mods, err := modules(pkg)
	if err != nil {
		return err
	}
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return fmt.Errorf("go env GOROOT: %w", err)
	}
	var b bytes.Buffer
	b.WriteString("Envoryx is licensed under the GNU Affero General Public License, version 3\n")
	b.WriteString("(see LICENSE, https://github.com/envoryx/envoryx). It contains the third-party\n")
	b.WriteString("software below, each under its own licence.\n\n")

	b.WriteString(rule + "\nGO MODULES (the envoryx program)\n")
	std, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(goroot)), "LICENSE"))
	if err != nil {
		return fmt.Errorf("the Go standard library's licence: %w", err)
	}
	entry(&b, "Go standard library", "https://go.dev", string(std))
	for _, m := range mods {
		texts, err := notices(m.dir)
		if err != nil {
			return err
		}
		if texts == "" {
			return fmt.Errorf("%s %s has no licence file; add its licence text by hand", m.path, m.version)
		}
		entry(&b, m.path+" "+m.version, "https://pkg.go.dev/"+m.path+"@"+m.version, texts)
	}

	web, err := os.ReadFile(webList)
	if err != nil {
		return fmt.Errorf("the web interface's list (build it first): %w", err)
	}
	b.WriteString("\n" + rule + "\nNPM PACKAGES (the web interface)\n")
	b.Write(web)
	return os.WriteFile(out, b.Bytes(), 0o644)
}

func entry(b *bytes.Buffer, title, url, text string) {
	fmt.Fprintf(b, "%s\n%s\n%s\n%s\n%s\n\n", rule, title, url, strings.Repeat("-", 80), strings.TrimSpace(text))
}

// modules lists the modules whose packages go into pkg, without the main module.
func modules(pkg string) ([]module, error) {
	cmd := exec.Command("go", "list", "-deps", "-f", `{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{with .Replace}}{{.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}`, pkg)
	cmd.Env = targetEnv
	cmd.Stderr = os.Stderr
	outp, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	seen := map[string]module{}
	sc := bufio.NewScanner(bytes.NewReader(outp))
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) != 3 {
			continue
		}
		seen[parts[0]] = module{parts[0], parts[1], parts[2]}
	}
	list := make([]module, 0, len(seen))
	for _, m := range seen {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].path < list[j].path })
	return list, nil
}

// notices joins the licence and notice files at the root of a module.
func notices(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var texts []string
	for _, e := range entries {
		if e.IsDir() || !noticeFile.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return "", err
		}
		texts = append(texts, strings.TrimSpace(string(data)))
	}
	return strings.Join(texts, "\n\n"), nil
}
