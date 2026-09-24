package offsite

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/s3"
)

// Object is a file on a target; Key is relative to the target's prefix.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

// Backend is what every target type offers. Keys use "/" and never start with one; the
// backend puts them below the target's prefix.
type Backend interface {
	// Put stores everything r yields under key, replacing an existing object.
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	// Get streams an object; the caller closes it.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns the objects below dir (one level; dir without trailing slash).
	List(ctx context.Context, dir string) ([]Object, error)
	// Delete removes an object; a missing one is not an error.
	Delete(ctx context.Context, key string) error
	// Close releases connections.
	Close() error
}

// ErrObjectNotFound is returned by Get for a missing key.
var ErrObjectNotFound = errors.New("not found on the target")

// Dialer opens a backend; tests replace it.
type Dialer func(ctx context.Context, t Target, pin func(fingerprint string)) (Backend, error)

// Dial opens the backend of a target. pin is called with the SFTP host key when the
// target has none recorded yet.
func Dial(ctx context.Context, t Target, pin func(fingerprint string)) (Backend, error) {
	switch t.Type {
	case TypeS3:
		return newS3(t), nil
	case TypeSFTP:
		return dialSFTP(ctx, t, pin)
	case TypeWebDAV:
		return newWebDAV(t), nil
	}
	return nil, fmt.Errorf("unknown target type %q", t.Type)
}

// transport is shared by the HTTP backends: no overall timeout (archives take as long as
// they take), but a connection and a server that stop answering are given up on.
func transport() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
	}}
}

// ---- S3 ------------------------------------------------------------------------

type s3Backend struct {
	c      *s3.Client
	bucket string
	prefix string
}

func newS3(t Target) *s3Backend {
	c := s3.NewClient(t.Endpoint, t.AccessKey, t.SecretKey)
	c.Region = t.Region
	c.HTTP = transport()
	return &s3Backend{c: c, bucket: t.Bucket, prefix: t.Prefix}
}

func (b *s3Backend) key(k string) string { return b.prefix + "/" + k }

func (b *s3Backend) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	return b.c.UploadStream(ctx, b.bucket, b.key(key), r)
}

func (b *s3Backend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, _, err := b.c.GetObject(ctx, b.bucket, b.key(key))
	var se *s3.Error
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return nil, ErrObjectNotFound
	}
	return rc, err
}

func (b *s3Backend) List(ctx context.Context, dir string) ([]Object, error) {
	prefix := b.key(dir) + "/"
	objs, err := b.c.ListPrefix(ctx, b.bucket, prefix)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, o := range objs {
		rest := strings.TrimPrefix(o.Key, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		out = append(out, Object{Key: dir + "/" + rest, Size: o.Size, Modified: o.LastModified})
	}
	return out, nil
}

func (b *s3Backend) Delete(ctx context.Context, key string) error {
	return b.c.DeleteObject(ctx, b.bucket, b.key(key))
}

func (b *s3Backend) Close() error { return nil }

// ---- WebDAV --------------------------------------------------------------------

type webdavBackend struct {
	base   *url.URL
	user   string
	pass   string
	prefix string
	http   *http.Client
}

func newWebDAV(t Target) *webdavBackend {
	u, _ := url.Parse(t.URL)
	return &webdavBackend{base: u, user: t.User, pass: t.Password, prefix: t.Prefix, http: transport()}
}

func (b *webdavBackend) url(p string) string {
	u := *b.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + p
	return u.String()
}

func (b *webdavBackend) request(ctx context.Context, method, p string, body io.Reader, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, b.url(p), body)
	if err != nil {
		return nil, err
	}
	if b.user != "" {
		req.SetBasicAuth(b.user, b.pass)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return b.http.Do(req)
}

func webdavError(method string, res *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	res.Body.Close()
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if res.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("webdav %s: the server refused the login (HTTP 401)", method)
	}
	return fmt.Errorf("webdav %s: HTTP %d %s", method, res.StatusCode, msg)
}

// mkdirs creates every collection on the way to dir; existing ones answer 405.
func (b *webdavBackend) mkdirs(ctx context.Context, dir string) error {
	parts := strings.Split(dir, "/")
	for i := range parts {
		res, err := b.request(ctx, "MKCOL", strings.Join(parts[:i+1], "/")+"/", nil, nil)
		if err != nil {
			return err
		}
		switch {
		case res.StatusCode == http.StatusCreated || res.StatusCode == http.StatusMethodNotAllowed || res.StatusCode == http.StatusOK:
			res.Body.Close()
		default:
			return webdavError("MKCOL", res)
		}
	}
	return nil
}

func (b *webdavBackend) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	full := b.prefix + "/" + key
	if err := b.mkdirs(ctx, path.Dir(full)); err != nil {
		return 0, err
	}
	cr := &countingReader{r: r}
	// The length is unknown, so the body goes chunked; Nextcloud, Apache mod_dav, nginx
	// and the Storage Box accept that.
	res, err := b.request(ctx, http.MethodPut, full, cr, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return 0, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return 0, webdavError("PUT", res)
	}
	res.Body.Close()
	return cr.n, nil
}

func (b *webdavBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	res, err := b.request(ctx, http.MethodGet, b.prefix+"/"+key, nil, nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return nil, ErrObjectNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, webdavError("GET", res)
	}
	return res.Body, nil
}

type multistatus struct {
	Responses []struct {
		Href  string `xml:"href"`
		Props []struct {
			Prop struct {
				Length       int64  `xml:"getcontentlength"`
				LastModified string `xml:"getlastmodified"`
				ResourceType struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
			Status string `xml:"status"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func (b *webdavBackend) List(ctx context.Context, dir string) ([]Object, error) {
	body := `<?xml version="1.0" encoding="utf-8"?><propfind xmlns="DAV:"><prop><getcontentlength/><getlastmodified/><resourcetype/></prop></propfind>`
	res, err := b.request(ctx, "PROPFIND", b.prefix+"/"+dir+"/", strings.NewReader(body), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return nil, nil
	}
	if res.StatusCode != http.StatusMultiStatus {
		return nil, webdavError("PROPFIND", res)
	}
	defer res.Body.Close()
	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("webdav PROPFIND: %w", err)
	}
	var out []Object
	for _, r := range ms.Responses {
		href, err := url.PathUnescape(r.Href)
		if err != nil {
			continue
		}
		name := path.Base(strings.TrimRight(href, "/"))
		if strings.HasSuffix(href, "/") {
			continue
		}
		o := Object{Key: dir + "/" + name}
		for _, ps := range r.Props {
			if ps.Prop.ResourceType.Collection != nil {
				o.Key = ""
				break
			}
			if ps.Prop.Length > 0 {
				o.Size = ps.Prop.Length
			}
			if t, err := http.ParseTime(ps.Prop.LastModified); err == nil {
				o.Modified = t
			}
		}
		if o.Key != "" {
			out = append(out, o)
		}
	}
	return out, nil
}

func (b *webdavBackend) Delete(ctx context.Context, key string) error {
	res, err := b.request(ctx, http.MethodDelete, b.prefix+"/"+key, nil, nil)
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusNotFound || (res.StatusCode >= 200 && res.StatusCode <= 299) {
		res.Body.Close()
		return nil
	}
	return webdavError("DELETE", res)
}

func (b *webdavBackend) Close() error { return nil }

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
