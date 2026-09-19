// Package s3 is the minimal S3 client Envoryx needs for a project's object storage:
// provisioning the bucket and its access policy, and moving objects in and out for
// backups. Requests are signed with AWS Signature Version 4; no SDK, since a handful of
// calls do not justify one.
package s3

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client talks to one S3-compatible endpoint with one set of credentials. Requests use
// path-style addressing (http://host/bucket/key), which every local server supports.
type Client struct {
	Endpoint  string // e.g. http://s3:9000
	Region    string
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	now       func() time.Time
}

// Error is a non-2xx answer from the server.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	return fmt.Sprintf("s3: HTTP %d: %s", e.Status, body)
}

// Provisioner is what the project manager needs from object storage; the real client
// implements it and tests substitute a fake.
type Provisioner interface {
	// EnsureBucket creates the bucket unless it exists and applies the public-read policy
	// (or removes it). It retries while the server is still starting, up to ctx's deadline.
	EnsureBucket(ctx context.Context, endpoint, accessKey, secretKey, bucket string, publicRead bool) error
}

// Default is the real provisioner.
type Default struct{}

// EnsureBucket implements Provisioner.
func (Default) EnsureBucket(ctx context.Context, endpoint, accessKey, secretKey, bucket string, publicRead bool) error {
	c := &Client{Endpoint: endpoint, Region: "us-east-1", AccessKey: accessKey, SecretKey: secretKey}
	var lastErr error
	for attempt := 0; ; attempt++ {
		lastErr = c.ensureBucket(ctx, bucket, publicRead)
		if lastErr == nil {
			return nil
		}
		var se *Error
		// A definite answer from the server is not going to change by waiting.
		if errors.As(lastErr, &se) && se.Status != http.StatusServiceUnavailable && se.Status != http.StatusInternalServerError {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last error: %v)", ctx.Err(), lastErr)
		case <-time.After(time.Duration(min(attempt+1, 5)) * 500 * time.Millisecond):
		}
	}
}

func (c *Client) ensureBucket(ctx context.Context, bucket string, publicRead bool) error {
	exists, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if !exists {
		if err := c.CreateBucket(ctx, bucket); err != nil {
			return err
		}
	}
	if publicRead {
		return c.PutBucketPolicy(ctx, bucket, PublicReadPolicy(bucket))
	}
	return c.DeleteBucketPolicy(ctx, bucket)
}

// PublicReadPolicy allows anonymous GetObject on every object of the bucket – what a
// public-read ACL gives on providers that honour it.
func PublicReadPolicy(bucket string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":"EnvoryxPublicRead","Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, bucket)
}

// BucketExists uses HEAD bucket.
func (c *Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	res, err := c.do(ctx, http.MethodHead, "/"+bucket, "", nil)
	if err != nil {
		var se *Error
		if errors.As(err, &se) && se.Status == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	res.Body.Close()
	return true, nil
}

// CreateBucket creates a bucket; an existing one owned by us is not an error.
func (c *Client) CreateBucket(ctx context.Context, bucket string) error {
	res, err := c.do(ctx, http.MethodPut, "/"+bucket, "", nil)
	if err != nil {
		var se *Error
		if errors.As(err, &se) && se.Status == http.StatusConflict && strings.Contains(se.Body, "BucketAlreadyOwnedByYou") {
			return nil
		}
		return err
	}
	res.Body.Close()
	return nil
}

// PutBucketPolicy replaces the bucket policy.
func (c *Client) PutBucketPolicy(ctx context.Context, bucket, policy string) error {
	res, err := c.do(ctx, http.MethodPut, "/"+bucket, "policy=", []byte(policy))
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

// DeleteBucketPolicy removes the bucket policy; none present is fine.
func (c *Client) DeleteBucketPolicy(ctx context.Context, bucket string) error {
	res, err := c.do(ctx, http.MethodDelete, "/"+bucket, "policy=", nil)
	if err != nil {
		var se *Error
		if errors.As(err, &se) && (se.Status == http.StatusNotFound || se.Status == http.StatusNoContent) {
			return nil
		}
		return err
	}
	res.Body.Close()
	return nil
}

// do sends one signed request; non-2xx answers become *Error.
func (c *Client) do(ctx context.Context, method, path, query string, body []byte) (*http.Response, error) {
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/") + path)
	if err != nil {
		return nil, err
	}
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	if err := c.Sign(req, body); err != nil {
		return nil, err
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		res.Body.Close()
		return nil, &Error{Status: res.StatusCode, Body: string(b)}
	}
	return res, nil
}

// Sign adds the SigV4 Authorization header (and the x-amz-* headers it covers) to req.
func (c *Client) Sign(req *http.Request, body []byte) error {
	if len(body) > 0 {
		req.ContentLength = int64(len(body))
	}
	return c.signWithHash(req, sha256Hex(body))
}

// signWithHash signs with a given payload hash (a real SHA-256 or UNSIGNED-PAYLOAD for
// streamed bodies).
func (c *Client) signWithHash(req *http.Request, payloadHash string) error {
	now := time.Now().UTC()
	if c.now != nil {
		now = c.now().UTC()
	}
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	// Canonical request: signed headers are host and every x-amz-* header we set.
	var names []string
	for k := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || strings.HasPrefix(lk, "x-amz-") {
			names = append(names, lk)
		}
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		v := req.URL.Host
		if n != "host" {
			v = strings.TrimSpace(req.Header.Get(n))
		}
		canonHeaders.WriteString(n + ":" + v + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method,
		uriEncodePath(req.URL.Path),
		canonicalQuery(req.URL.RawQuery),
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := date + "/" + c.Region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonical))}, "\n")
	kDate := hmacSHA256([]byte("AWS4"+c.SecretKey), date)
	kRegion := hmacSHA256(kDate, c.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKey, scope, signedHeaders, signature))
	return nil
}

// canonicalQuery sorts and encodes the query string the way SigV4 wants (a bare key
// becomes key=).
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	pairs := make([][2]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		ku, _ := url.QueryUnescape(k)
		vu, _ := url.QueryUnescape(v)
		pairs = append(pairs, [2]string{uriEncode(ku), uriEncode(vu)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p[0] + "=" + p[1]
	}
	return strings.Join(out, "&")
}

func uriEncodePath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}

// uriEncode is RFC 3986 encoding with the unreserved set AWS uses.
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// Noop is a Provisioner that does nothing – for tests and environments without a
// reachable server.
type Noop struct{}

// EnsureBucket implements Provisioner.
func (Noop) EnsureBucket(context.Context, string, string, string, string, bool) error { return nil }

// Object is one entry of a bucket listing.
type Object struct {
	Key  string
	Size int64
}

type listBucketResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
}

// ListObjects returns every object of the bucket (ListObjectsV2, all pages).
func (c *Client) ListObjects(ctx context.Context, bucket string) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := "list-type=2&max-keys=1000"
		if token != "" {
			q += "&continuation-token=" + url.QueryEscape(token)
		}
		res, err := c.do(ctx, http.MethodGet, "/"+bucket, q, nil)
		if err != nil {
			return nil, err
		}
		var page listBucketResult
		err = xml.NewDecoder(res.Body).Decode(&page)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3: decode listing: %w", err)
		}
		for _, o := range page.Contents {
			out = append(out, Object{Key: o.Key, Size: o.Size})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

// GetObject streams an object; the caller closes the body. The content type is the
// stored one ("" when the server sent none).
func (c *Client) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, string, error) {
	res, err := c.do(ctx, http.MethodGet, "/"+bucket+"/"+key, "", nil)
	if err != nil {
		return nil, "", err
	}
	return res.Body, res.Header.Get("Content-Type"), nil
}

// PutObject uploads an object of known size from a stream. The payload is not hashed
// (UNSIGNED-PAYLOAD), so the stream is read once; over the project network that is fine.
func (c *Client) PutObject(ctx context.Context, bucket, key string, body io.Reader, size int64, contentType string) error {
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/") + "/" + bucket + "/" + key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if err := c.signWithHash(req, "UNSIGNED-PAYLOAD"); err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return &Error{Status: res.StatusCode, Body: string(b)}
	}
	return nil
}

// DeleteObjects removes the given keys (batches of 1000, the API's limit).
func (c *Client) DeleteObjects(ctx context.Context, bucket string, keys []string) error {
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		var b strings.Builder
		b.WriteString(`<Delete xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Quiet>true</Quiet>`)
		for _, k := range keys[:n] {
			b.WriteString("<Object><Key>")
			xml.EscapeText(&b, []byte(k))
			b.WriteString("</Key></Object>")
		}
		b.WriteString("</Delete>")
		body := []byte(b.String())
		u, err := url.Parse(strings.TrimRight(c.Endpoint, "/") + "/" + bucket)
		if err != nil {
			return err
		}
		u.RawQuery = "delete="
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(b.String()))
		if err != nil {
			return err
		}
		// The multi-object delete requires a Content-MD5.
		sum := md5.Sum(body)
		req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
		req.Header.Set("Content-Type", "application/xml")
		if err := c.Sign(req, body); err != nil {
			return err
		}
		hc := c.HTTP
		if hc == nil {
			hc = &http.Client{Timeout: 5 * time.Minute}
		}
		res, err := hc.Do(req)
		if err != nil {
			return err
		}
		errBody, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode > 299 {
			return &Error{Status: res.StatusCode, Body: string(errBody)}
		}
		if strings.Contains(string(errBody), "<Error>") {
			return fmt.Errorf("s3: some objects were not deleted: %s", strings.TrimSpace(string(errBody)))
		}
		keys = keys[n:]
	}
	return nil
}

// ObjectStore is what backups need from a bucket: listing, streaming reads and writes,
// batch deletes. *Client implements it; tests use an in-memory stand-in.
type ObjectStore interface {
	ListObjects(ctx context.Context, bucket string) ([]Object, error)
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, string, error)
	PutObject(ctx context.Context, bucket, key string, body io.Reader, size int64, contentType string) error
	DeleteObjects(ctx context.Context, bucket string, keys []string) error
}

// NewClient returns a client for one endpoint and credential pair.
func NewClient(endpoint, accessKey, secretKey string) *Client {
	return &Client{Endpoint: endpoint, Region: "us-east-1", AccessKey: accessKey, SecretKey: secretKey}
}
