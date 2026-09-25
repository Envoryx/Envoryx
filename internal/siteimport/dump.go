package siteimport

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// DumpInfo describes an uploaded SQL dump.
type DumpInfo struct {
	Bytes      int64 `json:"bytes"`
	Compressed bool  `json:"compressed"`
	// Variant is the database the dump was taken from as far as its header tells:
	// "mariadb", "mysql" or "postgresql" ("" = plain SQL of unknown origin).
	Variant string `json:"variant,omitempty"`
	// Server is the server version the header names ("10.11.6-MariaDB", "8.0.35").
	Server string `json:"server,omitempty"`
	// Tool is what wrote the dump ("mysqldump", "phpMyAdmin", "pg_dump"…).
	Tool string `json:"tool,omitempty"`
}

var (
	mysqlServerRe = regexp.MustCompile(`(?im)^--\s*(?:Server version|Server-Version|Version du serveur|Serverversion)\s*:?\s*([0-9][0-9A-Za-z.+~_-]*)`)
	pgVersionRe   = regexp.MustCompile(`(?m)^-- Dumped from database version ([0-9.]+)`)
)

// openDump opens a dump for reading, gunzipping it when it is compressed.
func openDump(file string) (io.ReadCloser, bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, false, err
	}
	br := bufio.NewReaderSize(f, 64<<10)
	magic, _ := br.Peek(2)
	if bytes.Equal(magic, []byte{0x1f, 0x8b}) {
		gz, err := gzip.NewReader(br)
		if err != nil {
			_ = f.Close()
			return nil, false, fmt.Errorf("%w: read gzip dump: %v", validate.ErrInvalid, err)
		}
		return struct {
			io.Reader
			io.Closer
		}{gz, closers{gz, f}}, true, nil
	}
	return struct {
		io.Reader
		io.Closer
	}{br, f}, false, nil
}

type closers []io.Closer

func (c closers) Close() error {
	var first error
	for _, x := range c {
		if err := x.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// AnalyzeDump reads the head of a dump and tells where it comes from. Formats psql and
// mysql cannot read – pg_dump's custom format, a ZIP – are refused here, before anything
// is created.
func AnalyzeDump(file string) (DumpInfo, error) {
	st, err := os.Stat(file)
	if err != nil {
		return DumpInfo{}, err
	}
	info := DumpInfo{Bytes: st.Size()}
	r, compressed, err := openDump(file)
	if err != nil {
		return DumpInfo{}, err
	}
	defer r.Close()
	info.Compressed = compressed
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	switch {
	case n == 0:
		return DumpInfo{}, fmt.Errorf("%w: the database dump is empty", validate.ErrInvalid)
	case bytes.HasPrefix(head, []byte("PGDMP")):
		return DumpInfo{}, fmt.Errorf("%w: this is a pg_dump archive in custom format; export it as plain SQL (pg_dump --format=plain)", validate.ErrInvalid)
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return DumpInfo{}, fmt.Errorf("%w: the database dump is a ZIP archive; upload the .sql file inside it (or a .sql.gz)", validate.ErrInvalid)
	case bytes.IndexByte(head, 0) >= 0:
		return DumpInfo{}, fmt.Errorf("%w: the database dump is not an SQL text file", validate.ErrInvalid)
	}
	text := string(head)
	server := ""
	if m := mysqlServerRe.FindStringSubmatch(text); m != nil {
		server = m[1]
	}
	switch {
	case strings.Contains(text, "PostgreSQL database dump"):
		info.Variant, info.Tool = "postgresql", "pg_dump"
		if m := pgVersionRe.FindStringSubmatch(text); m != nil {
			info.Server = m[1]
		}
		return info, nil
	case strings.Contains(text, "phpMyAdmin SQL Dump"):
		info.Tool = "phpMyAdmin"
	case strings.Contains(text, "MariaDB dump"):
		info.Tool = "mariadb-dump"
	case strings.Contains(text, "MySQL dump"):
		info.Tool = "mysqldump"
	case strings.Contains(text, "Adminer"):
		info.Tool = "Adminer"
	}
	info.Server = server
	lower := strings.ToLower(text + " " + server)
	switch {
	case strings.Contains(lower, "mariadb"):
		info.Variant = "mariadb"
	case strings.Contains(lower, "utf8mb4_0900_") || majorOf(server) >= 8:
		// MySQL 8 collations do not exist in MariaDB: such a dump needs MySQL.
		info.Variant = "mysql"
	case info.Tool != "" || strings.Contains(text, "ENGINE=") || strings.Contains(text, "`"):
		info.Variant = "mariadb"
	}
	return info, nil
}

func majorOf(v string) int {
	parsed, parts, ok := parseVersion(v)
	if !ok || parts == 0 {
		return 0
	}
	return parsed[0]
}

// Statements that tie a dump to the server it came from: the database it was taken
// from (mysqldump --databases, pg_dump --create) and roles that do not exist here.
var (
	mysqlDropRe = regexp.MustCompile("(?i)^(CREATE DATABASE\\b|USE\\s+`?[^`;]+`?\\s*;)")
	pgDropRe    = regexp.MustCompile(`(?i)^(CREATE DATABASE\b|ALTER DATABASE\b|DROP DATABASE\b|\\connect\b|\\c\s|ALTER\s+.+\s+OWNER TO\s|GRANT\s|REVOKE\s|SET\s+ROLE\b|SET\s+SESSION\s+AUTHORIZATION\b|CREATE\s+ROLE\b|ALTER\s+ROLE\b|COMMENT ON EXTENSION\b)`)
)

// OpenDumpForImport returns the dump as the database client should read it: unpacked,
// and without the statements that would switch to the original database name or hand
// objects to roles that only existed on the old server. variant is the target's.
func OpenDumpForImport(file, variant string) (io.ReadCloser, error) {
	r, _, err := openDump(file)
	if err != nil {
		return nil, err
	}
	drop := mysqlDropRe
	if variant == "postgresql" {
		drop = pgDropRe
	}
	pr, pw := io.Pipe()
	go func() {
		br := bufio.NewReaderSize(r, 256<<10)
		bw := bufio.NewWriterSize(pw, 256<<10)
		// COPY … FROM stdin blocks hold table data, never statements to leave out.
		inCopy, midLine := false, false
		for {
			line, err := br.ReadSlice('\n')
			// Only whole short lines are statements worth looking at; a long line (an
			// extended INSERT) is passed on piece by piece as it comes.
			whole := err == nil && !midLine
			midLine = err == bufio.ErrBufferFull
			if midLine {
				err = nil
			}
			keep := true
			if whole && len(line) < 4096 {
				trimmed := bytes.TrimLeft(line, " \t")
				switch {
				case inCopy:
					inCopy = !bytes.Equal(bytes.TrimRight(trimmed, "\r\n"), []byte(`\.`))
				case variant == "postgresql" && bytes.HasPrefix(trimmed, []byte("COPY ")) && bytes.Contains(line, []byte("FROM stdin")):
					inCopy = true
				case drop.Match(trimmed):
					keep = false
				}
			}
			if keep && len(line) > 0 {
				if _, werr := bw.Write(line); werr != nil {
					_ = r.Close()
					_ = pw.CloseWithError(werr)
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					err = bw.Flush()
				}
				_ = r.Close()
				_ = pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr, nil
}
