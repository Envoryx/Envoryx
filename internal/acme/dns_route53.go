package acme

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/awssig"
)

// Route 53 keeps all values of a name and type in one resource record set, and a change
// replaces the whole set: the challenges for the domain and its wildcard share a name, so
// Present adds its value to what is there and Cleanup takes only its own value away.

const route53API = "https://route53.amazonaws.com/2013-04-01"

type route53 struct {
	accessKey string
	secretKey string
	zoneID    string
	http      *http.Client
	base      string
}

func (r *route53) do(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/xml")
	}
	awssig.Sign(req, awssig.PayloadHash(body), awssig.Credentials{AccessKey: r.accessKey, SecretKey: r.secretKey, Region: "us-east-1", Service: "route53"}, time.Now())
	res, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("route53: %w", err)
	}
	defer res.Body.Close()
	raw := readBody(res.Body)
	if res.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Code    string `xml:"Code"`
				Message string `xml:"Message"`
			} `xml:"Error"`
		}
		_ = xml.Unmarshal(raw, &e)
		msg := strings.TrimSpace(e.Error.Code + ": " + e.Error.Message)
		if e.Error.Code == "" {
			msg = fmt.Sprintf("HTTP %d: %.200s", res.StatusCode, raw)
		}
		if res.StatusCode == http.StatusForbidden {
			return fmt.Errorf("route53: access denied (%s); the IAM user needs the Route 53 permissions listed in the settings", msg)
		}
		return fmt.Errorf("route53: %s", msg)
	}
	if out != nil {
		if err := xml.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("route53: unexpected response: %.200s", raw)
		}
	}
	return nil
}

type r53Zone struct {
	ID     string `xml:"Id"`
	Name   string `xml:"Name"`
	Config struct {
		PrivateZone bool `xml:"PrivateZone"`
	} `xml:"Config"`
}

// zone returns the hosted zone id (without /hostedzone/) and its name.
func (r *route53) zone(ctx context.Context, fqdn string) (string, string, error) {
	if r.zoneID != "" {
		var res struct {
			Zone r53Zone `xml:"HostedZone"`
		}
		id := strings.TrimPrefix(r.zoneID, "/hostedzone/")
		if err := r.do(ctx, http.MethodGet, "/hostedzone/"+url.PathEscape(id), nil, &res); err != nil {
			return "", "", err
		}
		return id, strings.TrimSuffix(res.Zone.Name, "."), nil
	}
	for _, name := range zoneCandidates(fqdn) {
		var res struct {
			Zones []r53Zone `xml:"HostedZones>HostedZone"`
		}
		if err := r.do(ctx, http.MethodGet, "/hostedzonesbyname?maxitems=10&dnsname="+url.QueryEscape(name), nil, &res); err != nil {
			return "", "", err
		}
		for _, z := range res.Zones {
			if strings.EqualFold(strings.TrimSuffix(z.Name, "."), name) && !z.Config.PrivateZone {
				return strings.TrimPrefix(z.ID, "/hostedzone/"), name, nil
			}
		}
	}
	return "", "", fmt.Errorf("route53: no public hosted zone found for %s", fqdn)
}

type r53Set struct {
	Name    string   `xml:"Name"`
	Type    string   `xml:"Type"`
	TTL     int      `xml:"TTL"`
	Records []string `xml:"ResourceRecords>ResourceRecord>Value"`
}

// current returns the TXT set at fqdn (empty when there is none).
func (r *route53) current(ctx context.Context, zoneID, fqdn string) (r53Set, error) {
	var res struct {
		Sets []r53Set `xml:"ResourceRecordSets>ResourceRecordSet"`
	}
	name := strings.TrimSuffix(fqdn, ".") + "."
	if err := r.do(ctx, http.MethodGet, "/hostedzone/"+url.PathEscape(zoneID)+"/rrset?type=TXT&maxitems=1&name="+url.QueryEscape(name), nil, &res); err != nil {
		return r53Set{}, err
	}
	for _, s := range res.Sets {
		if strings.EqualFold(s.Name, name) && s.Type == "TXT" {
			return s, nil
		}
	}
	return r53Set{Name: name, Type: "TXT", TTL: 60}, nil
}

func (r *route53) change(ctx context.Context, zoneID, action string, set r53Set) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ChangeResourceRecordSetsRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><ChangeBatch><Comment>Envoryx ACME challenge</Comment><Changes><Change><Action>`)
	b.WriteString(action)
	b.WriteString(`</Action><ResourceRecordSet><Name>`)
	_ = xml.EscapeText(&b, []byte(set.Name))
	b.WriteString(`</Name><Type>TXT</Type><TTL>`)
	b.WriteString(strconv.Itoa(set.TTL))
	b.WriteString(`</TTL><ResourceRecords>`)
	for _, v := range set.Records {
		b.WriteString(`<ResourceRecord><Value>`)
		_ = xml.EscapeText(&b, []byte(v))
		b.WriteString(`</Value></ResourceRecord>`)
	}
	b.WriteString(`</ResourceRecords></ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`)
	return r.do(ctx, http.MethodPost, "/hostedzone/"+url.PathEscape(zoneID)+"/rrset", []byte(b.String()), nil)
}

// Present implements DNSProvider.
func (r *route53) Present(ctx context.Context, fqdn, value string) (string, error) {
	zoneID, _, err := r.zone(ctx, fqdn)
	if err != nil {
		return "", err
	}
	set, err := r.current(ctx, zoneID, fqdn)
	if err != nil {
		return "", err
	}
	if v := quoteTXT(value); !slices.Contains(set.Records, v) {
		set.Records = append(set.Records, v)
	}
	if err := r.change(ctx, zoneID, "UPSERT", set); err != nil {
		return "", err
	}
	return strings.Join([]string{zoneID, fqdn, value}, "\n"), nil
}

// Cleanup implements DNSProvider.
func (r *route53) Cleanup(ctx context.Context, handle string) error {
	parts := strings.SplitN(handle, "\n", 3)
	if len(parts) != 3 {
		return errors.New("route53: bad record handle")
	}
	set, err := r.current(ctx, parts[0], parts[1])
	if err != nil {
		return err
	}
	rest := slices.DeleteFunc(slices.Clone(set.Records), func(v string) bool { return v == quoteTXT(parts[2]) })
	switch {
	case len(rest) == len(set.Records):
		return nil // not there (any more)
	case len(rest) == 0:
		// A deletion must name the set exactly as it is.
		return r.change(ctx, parts[0], "DELETE", set)
	default:
		set.Records = rest
		return r.change(ctx, parts[0], "UPSERT", set)
	}
}
