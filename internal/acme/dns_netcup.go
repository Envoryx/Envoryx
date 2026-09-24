package acme

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// netcup's CCP DNS API is one endpoint with actions: login yields a session, records are
// added and deleted through updateDnsRecords. Changes reach netcup's name servers only
// after several minutes, hence the long propagation wait of this provider.

const netcupAPI = "https://ccp.netcup.net/run/webservice/servers/endpoint.php?JSON"

type netcup struct {
	customer string
	apiKey   string
	password string
	http     *http.Client
	endpoint string
}

type netcupRecord struct {
	ID           string `json:"id,omitempty"`
	Hostname     string `json:"hostname"`
	Type         string `json:"type"`
	Priority     string `json:"priority,omitempty"`
	Destination  string `json:"destination"`
	DeleteRecord bool   `json:"deleterecord,omitempty"`
}

func (n *netcup) call(ctx context.Context, action string, param map[string]any, out any) error {
	status, raw, err := jsonRequest(ctx, n.http, http.MethodPost, n.endpoint, map[string]any{"action": action, "param": param}, nil)
	if err != nil {
		return fmt.Errorf("netcup: %w", err)
	}
	var res struct {
		Status       string          `json:"status"`
		StatusCode   int             `json:"statuscode"`
		ShortMessage string          `json:"shortmessage"`
		LongMessage  string          `json:"longmessage"`
		ResponseData json.RawMessage `json:"responsedata"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("netcup: unexpected response (HTTP %d): %.200s", status, raw)
	}
	if res.Status != "success" {
		msg := strings.TrimSpace(res.ShortMessage + " " + res.LongMessage)
		return fmt.Errorf("netcup %s: %s (%d)", action, msg, res.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(res.ResponseData, out)
	}
	return nil
}

// session logs in and returns the parameters every later call carries plus a logout.
func (n *netcup) session(ctx context.Context) (map[string]any, func(), error) {
	var login struct {
		Session string `json:"apisessionid"`
	}
	if err := n.call(ctx, "login", map[string]any{"customernumber": n.customer, "apikey": n.apiKey, "apipassword": n.password}, &login); err != nil {
		return nil, nil, err
	}
	base := map[string]any{"customernumber": n.customer, "apikey": n.apiKey, "apisessionid": login.Session}
	logout := func() { _ = n.call(context.WithoutCancel(ctx), "logout", base, nil) }
	return base, logout, nil
}

func with(base map[string]any, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (n *netcup) records(ctx context.Context, base map[string]any, zone string) ([]netcupRecord, error) {
	var res struct {
		Records []netcupRecord `json:"dnsrecords"`
	}
	if err := n.call(ctx, "infoDnsRecords", with(base, map[string]any{"domainname": zone}), &res); err != nil {
		return nil, err
	}
	return res.Records, nil
}

// Present implements DNSProvider.
func (n *netcup) Present(ctx context.Context, fqdn, value string) (string, error) {
	base, logout, err := n.session(ctx)
	if err != nil {
		return "", err
	}
	defer logout()
	zone := ""
	var lastErr error
	for _, name := range zoneCandidates(fqdn) {
		if _, err := n.records(ctx, base, name); err != nil {
			lastErr = err
			continue
		}
		zone = name
		break
	}
	if zone == "" {
		return "", fmt.Errorf("netcup: no domain found for %s on this customer account: %v", fqdn, lastErr)
	}
	rec := netcupRecord{Hostname: relativeName(fqdn, zone), Type: "TXT", Destination: value}
	if err := n.call(ctx, "updateDnsRecords", with(base, map[string]any{"domainname": zone, "dnsrecordset": map[string]any{"dnsrecords": []netcupRecord{rec}}}), nil); err != nil {
		return "", err
	}
	return strings.Join([]string{zone, rec.Hostname, value}, "\n"), nil
}

// Cleanup implements DNSProvider.
func (n *netcup) Cleanup(ctx context.Context, handle string) error {
	parts := strings.SplitN(handle, "\n", 3)
	if len(parts) != 3 {
		return errors.New("netcup: bad record handle")
	}
	base, logout, err := n.session(ctx)
	if err != nil {
		return err
	}
	defer logout()
	recs, err := n.records(ctx, base, parts[0])
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Type == "TXT" && strings.EqualFold(r.Hostname, parts[1]) && r.Destination == parts[2] {
			r.DeleteRecord = true
			return n.call(ctx, "updateDnsRecords", with(base, map[string]any{"domainname": parts[0], "dnsrecordset": map[string]any{"dnsrecords": []netcupRecord{r}}}), nil)
		}
	}
	return nil
}
