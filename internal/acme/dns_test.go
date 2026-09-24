package acme

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/envoryx/envoryx/internal/tlsca"
)

// txtZone is the state every fake provider keeps: TXT values by relative name in the
// zone example.com.
type txtZone struct {
	mu     sync.Mutex
	values map[string][]string
	nextID int
	ids    map[string][2]string // record id → name, value
}

func newZone() *txtZone {
	return &txtZone{values: map[string][]string{}, ids: map[string][2]string{}}
}

func (z *txtZone) add(name, value string) string {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.values[name] = append(z.values[name], value)
	z.nextID++
	id := strconv.Itoa(z.nextID)
	z.ids[id] = [2]string{name, value}
	return id
}

func (z *txtZone) remove(name, value string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.values[name] = slices.DeleteFunc(z.values[name], func(v string) bool { return v == value })
}

func (z *txtZone) removeID(id string) bool {
	z.mu.Lock()
	rec, ok := z.ids[id]
	delete(z.ids, id)
	z.mu.Unlock()
	if ok {
		z.remove(rec[0], rec[1])
	}
	return ok
}

func (z *txtZone) get(name string) []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return slices.Clone(z.values[name])
}

// exerciseProvider checks the two challenges of a wildcard order: both values on one
// name, removed one at a time.
func exerciseProvider(t *testing.T, p DNSProvider, z *txtZone) {
	t.Helper()
	ctx := context.Background()
	const fqdn = "_acme-challenge.dev.example.com"
	h1, err := p.Present(ctx, fqdn, "value-one")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := p.Present(ctx, fqdn, "value-two")
	if err != nil {
		t.Fatal(err)
	}
	if got := z.get("_acme-challenge.dev"); !slices.Equal(got, []string{"value-one", "value-two"}) {
		t.Fatalf("after presenting both: %v", got)
	}
	if err := p.Cleanup(ctx, h1); err != nil {
		t.Fatal(err)
	}
	if got := z.get("_acme-challenge.dev"); !slices.Equal(got, []string{"value-two"}) {
		t.Fatalf("after the first cleanup: %v", got)
	}
	if err := p.Cleanup(ctx, h2); err != nil {
		t.Fatal(err)
	}
	if got := z.get("_acme-challenge.dev"); len(got) != 0 {
		t.Fatalf("after both cleanups: %v", got)
	}
}

func writeJSONBody(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestHetzner(t *testing.T) {
	z := newZone()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hz-token" {
			writeJSONBody(w, 401, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "unable to authenticate"}})
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodGet && parts[0] == "zones" && len(parts) == 2:
			if parts[1] != "example.com" {
				writeJSONBody(w, 404, map[string]any{"error": map[string]string{"code": "not_found", "message": "zone not found"}})
				return
			}
			writeJSONBody(w, 200, map[string]any{"zone": map[string]any{"name": "example.com"}})
		case r.Method == http.MethodPost && len(parts) == 7 && parts[4] == "TXT":
			var body struct {
				TTL     *int `json:"ttl"`
				Records []struct {
					Value string `json:"value"`
				} `json:"records"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, rec := range body.Records {
				v, err := strconv.Unquote(rec.Value)
				if err != nil {
					writeJSONBody(w, 422, map[string]any{"error": map[string]string{"code": "invalid_input", "message": "TXT values must be quoted"}})
					return
				}
				if parts[6] == "add_records" {
					if body.TTL == nil {
						writeJSONBody(w, 422, map[string]any{"error": map[string]string{"code": "invalid_input", "message": "ttl missing"}})
						return
					}
					z.add(parts[3], v)
				} else {
					z.remove(parts[3], v)
				}
			}
			writeJSONBody(w, 201, map[string]any{"action": map[string]any{"id": 7, "status": "running"}})
		case r.Method == http.MethodGet && r.URL.Path == "/actions/7":
			writeJSONBody(w, 200, map[string]any{"action": map[string]any{"id": 7, "status": "success"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p, _ := newProvider("hetzner", map[string]string{"token": "hz-token"}, srv.Client(), srv.URL)
	exerciseProvider(t, p, z)
	bad, _ := newProvider("hetzner", map[string]string{"token": "old-dns-console-token"}, srv.Client(), srv.URL)
	if _, err := bad.Present(context.Background(), "_acme-challenge.dev.example.com", "v"); err == nil || !strings.Contains(err.Error(), "token rejected") {
		t.Fatalf("wrong token: %v", err)
	}
}

func TestDigitalOcean(t *testing.T) {
	z := newZone()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer do-token" {
			writeJSONBody(w, 401, map[string]string{"id": "unauthorized", "message": "Unable to authenticate you."})
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodGet && len(parts) == 2:
			if parts[1] != "example.com" {
				writeJSONBody(w, 404, map[string]string{"id": "not_found", "message": "The resource you requested could not be found."})
				return
			}
			writeJSONBody(w, 200, map[string]any{"domain": map[string]string{"name": "example.com"}})
		case r.Method == http.MethodPost && len(parts) == 3:
			var body struct{ Type, Name, Data string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			id, _ := strconv.Atoi(z.add(body.Name, body.Data))
			writeJSONBody(w, 201, map[string]any{"domain_record": map[string]any{"id": id}})
		case r.Method == http.MethodDelete && len(parts) == 4:
			if !z.removeID(parts[3]) {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p, _ := newProvider("digitalocean", map[string]string{"token": "do-token"}, srv.Client(), srv.URL)
	exerciseProvider(t, p, z)
}

func TestPorkbun(t *testing.T) {
	z := newZone()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["apikey"] != "pk1_x" || body["secretapikey"] != "sk1_y" {
			writeJSONBody(w, 400, map[string]string{"status": "ERROR", "message": "Invalid API key. (002)"})
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if parts[2] != "example.com" {
			writeJSONBody(w, 400, map[string]string{"status": "ERROR", "message": "Invalid domain."})
			return
		}
		switch parts[1] {
		case "retrieve":
			writeJSONBody(w, 200, map[string]any{"status": "SUCCESS", "records": []any{}})
		case "create":
			if body["ttl"] == "" || body["type"] != "TXT" {
				writeJSONBody(w, 400, map[string]string{"status": "ERROR", "message": "bad record"})
				return
			}
			id, _ := strconv.Atoi(z.add(body["name"], body["content"]))
			writeJSONBody(w, 200, map[string]any{"status": "SUCCESS", "id": id})
		case "delete":
			z.removeID(parts[3])
			writeJSONBody(w, 200, map[string]string{"status": "SUCCESS"})
		}
	}))
	defer srv.Close()
	p, _ := newProvider("porkbun", map[string]string{"apiKey": "pk1_x", "secretApiKey": "sk1_y"}, srv.Client(), srv.URL)
	exerciseProvider(t, p, z)
	bad, _ := newProvider("porkbun", map[string]string{"apiKey": "pk1_x", "secretApiKey": "nope"}, srv.Client(), srv.URL)
	if _, err := bad.Present(context.Background(), "_acme-challenge.dev.example.com", "v"); err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestNetcup(t *testing.T) {
	z := newZone()
	sessions := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string          `json:"action"`
			Param  json.RawMessage `json:"param"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var p struct {
			Customer   string `json:"customernumber"`
			APIKey     string `json:"apikey"`
			Password   string `json:"apipassword"`
			Session    string `json:"apisessionid"`
			DomainName string `json:"domainname"`
			Set        struct {
				Records []netcupRecord `json:"dnsrecords"`
			} `json:"dnsrecordset"`
		}
		_ = json.Unmarshal(req.Param, &p)
		fail := func(msg string) {
			writeJSONBody(w, 200, map[string]any{"status": "error", "statuscode": 4013, "shortmessage": msg, "action": req.Action})
		}
		ok := func(data any) {
			writeJSONBody(w, 200, map[string]any{"status": "success", "statuscode": 2000, "action": req.Action, "responsedata": data})
		}
		switch req.Action {
		case "login":
			if p.Customer != "12345" || p.APIKey != "key" || p.Password != "pw" {
				fail("Api key or password invalid")
				return
			}
			sessions++
			ok(map[string]string{"apisessionid": "s1"})
		case "logout":
			sessions--
			ok(nil)
		case "infoDnsRecords":
			if p.Session != "s1" || p.DomainName != "example.com" {
				fail("Domain not found")
				return
			}
			var recs []netcupRecord
			z.mu.Lock()
			for id, rec := range z.ids {
				recs = append(recs, netcupRecord{ID: id, Hostname: rec[0], Type: "TXT", Destination: rec[1]})
			}
			z.mu.Unlock()
			ok(map[string]any{"dnsrecords": recs})
		case "updateDnsRecords":
			for _, rec := range p.Set.Records {
				if rec.DeleteRecord {
					z.removeID(rec.ID)
				} else {
					z.add(rec.Hostname, rec.Destination)
				}
			}
			ok(map[string]any{"dnsrecords": []any{}})
		}
	}))
	defer srv.Close()
	p, _ := newProvider("netcup", map[string]string{"customerNumber": "12345", "apiKey": "key", "apiPassword": "pw"}, srv.Client(), srv.URL)
	exerciseProvider(t, p, z)
	if sessions != 0 {
		t.Fatalf("every session must be logged out: %d open", sessions)
	}
}

func TestRoute53(t *testing.T) {
	z := newZone()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIA1/") || !strings.Contains(r.Header.Get("Authorization"), "/us-east-1/route53/aws4_request") {
			w.WriteHeader(403)
			fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code><Message>denied</Message></Error></ErrorResponse>`)
			return
		}
		switch {
		case r.URL.Path == "/hostedzonesbyname":
			fmt.Fprint(w, `<ListHostedZonesByNameResponse><HostedZones>`)
			// A private zone of the same name comes first and must be skipped.
			if r.URL.Query().Get("dnsname") == "example.com" {
				fmt.Fprint(w, `<HostedZone><Id>/hostedzone/ZPRIV</Id><Name>example.com.</Name><Config><PrivateZone>true</PrivateZone></Config></HostedZone>`)
				fmt.Fprint(w, `<HostedZone><Id>/hostedzone/Z1</Id><Name>example.com.</Name><Config><PrivateZone>false</PrivateZone></Config></HostedZone>`)
			}
			fmt.Fprint(w, `</HostedZones></ListHostedZonesByNameResponse>`)
		case r.URL.Path == "/hostedzone/Z1/rrset" && r.Method == http.MethodGet:
			vals := z.get("_acme-challenge.dev")
			fmt.Fprint(w, `<ListResourceRecordSetsResponse><ResourceRecordSets>`)
			if len(vals) > 0 {
				fmt.Fprint(w, `<ResourceRecordSet><Name>_acme-challenge.dev.example.com.</Name><Type>TXT</Type><TTL>60</TTL><ResourceRecords>`)
				for _, v := range vals {
					fmt.Fprintf(w, `<ResourceRecord><Value>%s</Value></ResourceRecord>`, strconv.Quote(v))
				}
				fmt.Fprint(w, `</ResourceRecords></ResourceRecordSet>`)
			}
			fmt.Fprint(w, `</ResourceRecordSets></ListResourceRecordSetsResponse>`)
		case r.URL.Path == "/hostedzone/Z1/rrset" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Changes []struct {
					Action string `xml:"Action"`
					Set    struct {
						Name   string   `xml:"Name"`
						Values []string `xml:"ResourceRecords>ResourceRecord>Value"`
					} `xml:"ResourceRecordSet"`
				} `xml:"ChangeBatch>Changes>Change"`
			}
			if err := xml.Unmarshal(raw, &req); err != nil || len(req.Changes) != 1 {
				w.WriteHeader(400)
				return
			}
			c := req.Changes[0]
			var vals []string
			for _, v := range c.Set.Values {
				u, _ := strconv.Unquote(v)
				vals = append(vals, u)
			}
			if c.Action == "DELETE" && !slices.Equal(vals, z.get("_acme-challenge.dev")) {
				w.WriteHeader(400)
				fmt.Fprint(w, `<ErrorResponse><Error><Code>InvalidChangeBatch</Code><Message>values do not match</Message></Error></ErrorResponse>`)
				return
			}
			z.mu.Lock()
			if c.Action == "DELETE" {
				delete(z.values, "_acme-challenge.dev")
			} else {
				z.values["_acme-challenge.dev"] = vals
			}
			z.mu.Unlock()
			fmt.Fprint(w, `<ChangeResourceRecordSetsResponse><ChangeInfo><Id>/change/C1</Id><Status>PENDING</Status></ChangeInfo></ChangeResourceRecordSetsResponse>`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p, _ := newProvider("route53", map[string]string{"accessKeyId": "AKIA1", "secretAccessKey": "secret"}, srv.Client(), srv.URL)
	exerciseProvider(t, p, z)
}

func TestZoneCandidates(t *testing.T) {
	if got := zoneCandidates("_acme-challenge.dev.example.co.uk."); !slices.Equal(got, []string{"dev.example.co.uk", "example.co.uk", "co.uk"}) {
		t.Fatalf("candidates: %v", got)
	}
	if got := relativeName("_acme-challenge.dev.example.com", "example.com"); got != "_acme-challenge.dev" {
		t.Fatalf("relative: %q", got)
	}
}

func TestCredentialsPerProvider(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetConfig(Config{Provider: "netcup", Domain: "dev.example.com", Email: "me@example.com", Credentials: map[string]string{"customerNumber": "12345", "apiKey": "k"}}); err == nil || !strings.Contains(err.Error(), "API password") {
		t.Fatalf("a missing field must be named: %v", err)
	}
	if err := m.SetConfig(Config{Provider: "netcup", Domain: "dev.example.com", Email: "me@example.com", Credentials: map[string]string{"customerNumber": "12345", "apiKey": "k", "apiPassword": "p", "token": "x"}}); err == nil {
		t.Fatal("a field the provider does not have must be refused")
	}
	if err := m.SetConfig(Config{Provider: "netcup", Domain: "dev.example.com", Email: "me@example.com", Credentials: map[string]string{"customerNumber": "12345", "apiKey": "k", "apiPassword": "p"}}); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.Fields["customerNumber"] != "12345" || !slices.Equal(st.Secrets, []string{"apiKey", "apiPassword"}) {
		t.Fatalf("status: %+v", st)
	}
	// Secrets stay when left empty; switching providers asks for them again.
	if err := m.SetConfig(Config{Provider: "netcup", Domain: "dev.example.com", Email: "me@example.com", Credentials: map[string]string{"customerNumber": "54321"}}); err != nil {
		t.Fatal(err)
	}
	if c := m.Config().Credentials; c["apiPassword"] != "p" || c["customerNumber"] != "54321" {
		t.Fatalf("merge: %v", c)
	}
	if err := m.SetConfig(Config{Provider: "hetzner", Domain: "dev.example.com", Email: "me@example.com"}); err == nil {
		t.Fatal("another provider needs its own credentials")
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	certs, err := tlsca.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(t.TempDir(), certs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLegacyTokenIsMigrated(t *testing.T) {
	certs, _ := tlsca.Open(t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte(`{"provider":"cloudflare","domain":"dev.example.com","email":"me@example.com","token":"cf-old","staging":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(dir, certs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Config().Credentials["token"] != "cf-old" || !slices.Equal(m.Status().Secrets, []string{"token"}) {
		t.Fatalf("migration: %+v", m.Config())
	}
	// An old client sending "token" still works.
	if err := m.SetConfig(Config{Provider: "cloudflare", Domain: "dev.example.com", Email: "me@example.com", Token: "cf-new"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, configFile))
	if !strings.Contains(string(raw), `"credentials":{"token":"cf-new"}`) || strings.Contains(string(raw), `"token":"cf-old"`) {
		t.Fatalf("stored: %s", raw)
	}
}
