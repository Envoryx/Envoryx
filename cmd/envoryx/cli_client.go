// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/coder/websocket"
)

// client talks to a running Envoryx over its REST API. Everything the CLI does goes
// through the same endpoints the web interface uses, authenticated with an API token –
// there is no second, privileged path into the server.
type client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// apiError is a refusal from the server, carrying the reason the API stated (for example
// "this token has read scope, the operation needs operate") so the CLI can print it
// instead of a bare status code.
type apiError struct {
	Status  int
	Code    string
	Message string
	// Body is the answer as it arrived; a failed git command carries its output there.
	Body []byte
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("server returned %d", e.Status)
	}
	return e.Message
}

func newClient(cfg cliConfig) (*client, error) {
	base, err := url.Parse(strings.TrimSuffix(cfg.URL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid server address %q: use http(s)://host:port", cfg.URL)
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.Insecure} //nolint:gosec // opt-in, see --insecure
	if cfg.CACert != "" {
		pem, err := os.ReadFile(cfg.CACert)
		if err != nil {
			return nil, fmt.Errorf("CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA certificate %s contains no certificate", cfg.CACert)
		}
		tlsCfg.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg
	return &client{
		base:  base,
		token: cfg.Token,
		// No client-side timeout: logs --follow, backups and long actions run for
		// minutes. Each request carries a context instead, cancelled by Ctrl+C.
		http: &http.Client{Transport: transport},
	}, nil
}

func (c *client) url(path string, query url.Values) string {
	u := *c.base
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	u.RawQuery = query.Encode()
	return u.String()
}

// request sends one API request. body is encoded as JSON when it is not nil.
func (c *client) request(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path, query), payload)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "envoryx-cli/"+version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, friendlyDialError(err, c.base)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, decodeAPIError(resp)
	}
	return resp, nil
}

// do sends a request and decodes the JSON response into out (nil discards it).
func (c *client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, err := c.request(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("unreadable response from %s: %w", c.base.Host, err)
	}
	return nil
}

// upload sends a streamed body (a multipart form) and decodes the JSON answer into out.
func (c *client) upload(ctx context.Context, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path, nil), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "envoryx-cli/"+version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return friendlyDialError(err, c.base)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return decodeAPIError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("unreadable response from %s: %w", c.base.Host, err)
	}
	return nil
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

func (c *client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

// dialWS opens one of the API's WebSocket endpoints (log following, actions). A Go
// client sends no Origin header, so the server's origin check passes; the token travels
// in the Authorization header like everywhere else.
func (c *client) dialWS(ctx context.Context, path string, query url.Values) (*websocket.Conn, error) {
	target := c.url(path, query)
	switch c.base.Scheme {
	case "https":
		target = "wss" + strings.TrimPrefix(target, "https")
	default:
		target = "ws" + strings.TrimPrefix(target, "http")
	}
	header := http.Header{"User-Agent": {"envoryx-cli/" + version}}
	if c.token != "" {
		header.Set("Authorization", "Bearer "+c.token)
	}
	conn, resp, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: header})
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			return nil, decodeAPIError(resp)
		}
		return nil, friendlyDialError(err, c.base)
	}
	conn.SetReadLimit(4 << 20)
	return conn, nil
}

func decodeAPIError(resp *http.Response) error {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(raw, &body)
	err := &apiError{Status: resp.StatusCode, Code: body.Error.Code, Message: body.Error.Message, Body: raw}
	if err.Message == "" {
		err.Message = fmt.Sprintf("%s (%s)", strings.ToLower(http.StatusText(resp.StatusCode)), resp.Request.URL.Path)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		err.Message += "\nCheck the token: envoryx login --url " + resp.Request.URL.Scheme + "://" + resp.Request.URL.Host
	case http.StatusNotFound:
		if body.Error.Code == "" {
			err.Message = "no such route – is " + resp.Request.URL.Host + " an Envoryx server?"
		}
	}
	return err
}

// friendlyDialError turns the usual connection failures into an answer the reader can
// act on: the wrong address, a server that is not running, or Envoryx's own certificate
// authority not being trusted on this machine.
func friendlyDialError(err error, base *url.URL) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	var certErr x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	switch {
	case errors.As(err, &certErr), errors.As(err, &hostErr):
		return fmt.Errorf("%s uses a certificate this machine does not trust: pass --ca-cert with the file from Settings → TLS (or --insecure on a trusted network)\n%w", base.Host, err)
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return fmt.Errorf("cannot reach %s: %w", base.Host, netErr.Err)
	}
	return err
}
