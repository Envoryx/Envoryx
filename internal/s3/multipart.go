package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Offsite backups stream archives of unknown size (compressed, maybe encrypted) to a
// provider's bucket. A plain PUT needs the length up front and stops at 5 GB, so the
// stream is cut into parts and sent as a multipart upload; a stream that fits into one
// part goes up as a single PUT.

// PartSize is the size of one uploaded part. 16 MiB × the 10,000 parts S3 allows is
// about 156 GiB per archive, well above what a development project produces, and one
// part is all that is held in memory.
const PartSize = 16 << 20

// UploadStream uploads everything r yields as one object.
func (c *Client) UploadStream(ctx context.Context, bucket, key string, r io.Reader) (int64, error) {
	buf := make([]byte, PartSize)
	n, err := io.ReadFull(r, buf)
	switch {
	case err == io.EOF || err == io.ErrUnexpectedEOF:
		// Everything fits into one part.
		if err := c.PutObject(ctx, bucket, key, bytes.NewReader(buf[:n]), int64(n), "application/octet-stream"); err != nil {
			return 0, err
		}
		return int64(n), nil
	case err != nil:
		return 0, err
	}

	uploadID, err := c.createMultipart(ctx, bucket, key)
	if err != nil {
		return 0, err
	}
	abort := func(cause error) (int64, error) {
		// A dangling upload is billed until the provider's lifecycle rule removes it.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = c.do(actx, http.MethodDelete, "/"+bucket+"/"+key, "uploadId="+url.QueryEscape(uploadID), nil)
		return 0, cause
	}
	var (
		etags []string
		total int64
	)
	for part := 1; ; part++ {
		if part > 10000 {
			return abort(fmt.Errorf("s3: archive larger than %d GiB", int64(PartSize)*10000>>30))
		}
		etag, err := c.uploadPart(ctx, bucket, key, uploadID, part, buf[:n])
		if err != nil {
			return abort(fmt.Errorf("s3: part %d: %w", part, err))
		}
		etags = append(etags, etag)
		total += int64(n)
		n, err = io.ReadFull(r, buf)
		if err == io.EOF {
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return abort(err)
		}
	}
	if err := c.completeMultipart(ctx, bucket, key, uploadID, etags); err != nil {
		return abort(err)
	}
	return total, nil
}

func (c *Client) createMultipart(ctx context.Context, bucket, key string) (string, error) {
	res, err := c.do(ctx, http.MethodPost, "/"+bucket+"/"+key, "uploads=", nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(res.Body).Decode(&out); err != nil || out.UploadID == "" {
		return "", fmt.Errorf("s3: no upload id in the answer to CreateMultipartUpload")
	}
	return out.UploadID, nil
}

func (c *Client) uploadPart(ctx context.Context, bucket, key, uploadID string, part int, data []byte) (string, error) {
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/") + "/" + bucket + "/" + key)
	if err != nil {
		return "", err
	}
	u.RawQuery = "partNumber=" + strconv.Itoa(part) + "&uploadId=" + url.QueryEscape(uploadID)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		// The part is in memory, so it is signed with its real hash.
		if err := c.Sign(req, data); err != nil {
			return "", err
		}
		res, err := c.httpClient(10 * time.Minute).Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			res.Body.Close()
			if res.StatusCode >= 200 && res.StatusCode <= 299 {
				etag := res.Header.Get("ETag")
				if etag == "" {
					return "", fmt.Errorf("s3: no ETag for part %d", part)
				}
				return etag, nil
			}
			err = &Error{Status: res.StatusCode, Body: string(body)}
			if res.StatusCode < 500 {
				return "", err
			}
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return "", lastErr
}

func (c *Client) completeMultipart(ctx context.Context, bucket, key, uploadID string, etags []string) error {
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for i, etag := range etags {
		fmt.Fprintf(&b, "<Part><PartNumber>%d</PartNumber><ETag>", i+1)
		xml.EscapeText(&b, []byte(etag))
		b.WriteString("</ETag></Part>")
	}
	b.WriteString("</CompleteMultipartUpload>")
	res, err := c.do(ctx, http.MethodPost, "/"+bucket+"/"+key, "uploadId="+url.QueryEscape(uploadID), []byte(b.String()))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	// S3 answers 200 and reports a failure in the body when it happens late.
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
	if strings.Contains(string(body), "<Error>") {
		return &Error{Status: http.StatusOK, Body: string(body)}
	}
	return nil
}

func (c *Client) httpClient(timeout time.Duration) *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: timeout}
}

// ObjectInfo is a listed object with its modification time.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// ListPrefix lists the objects whose key starts with prefix (all pages).
func (c *Client) ListPrefix(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	token := ""
	for {
		q := "list-type=2&max-keys=1000&prefix=" + url.QueryEscape(prefix)
		if token != "" {
			q += "&continuation-token=" + url.QueryEscape(token)
		}
		res, err := c.do(ctx, http.MethodGet, "/"+bucket, q, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
			Contents              []struct {
				Key          string    `xml:"Key"`
				Size         int64     `xml:"Size"`
				LastModified time.Time `xml:"LastModified"`
			} `xml:"Contents"`
		}
		err = xml.NewDecoder(res.Body).Decode(&page)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3: decode listing: %w", err)
		}
		for _, o := range page.Contents {
			out = append(out, ObjectInfo{Key: o.Key, Size: o.Size, LastModified: o.LastModified})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

// DeleteObject removes one object; a missing one is not an error.
func (c *Client) DeleteObject(ctx context.Context, bucket, key string) error {
	res, err := c.do(ctx, http.MethodDelete, "/"+bucket+"/"+key, "", nil)
	if err != nil {
		return err
	}
	return res.Body.Close()
}
