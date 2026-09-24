// Package awssig signs HTTP requests with AWS Signature Version 4 – for S3-compatible
// storage and for Route 53. No SDK: two services and one algorithm do not justify one.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credentials sign for one service in one region.
type Credentials struct {
	AccessKey string
	SecretKey string
	Region    string
	Service   string // "s3", "route53"
}

// UnsignedPayload is the payload hash for streamed bodies that are not hashed.
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// PayloadHash is the SHA-256 of a body in the form the signature wants.
func PayloadHash(body []byte) string { return SHA256Hex(body) }

// Sign adds the Authorization header (and the x-amz-* headers it covers) to req.
// payloadHash is PayloadHash(body) or UnsignedPayload.
func Sign(req *http.Request, payloadHash string, c Credentials, now time.Time) {
	now = now.UTC()
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
		URIEncodePath(req.URL.Path),
		CanonicalQuery(req.URL.RawQuery),
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := date + "/" + c.Region + "/" + c.Service + "/aws4_request"
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, SHA256Hex([]byte(canonical))}, "\n")
	kDate := hmacSHA256([]byte("AWS4"+c.SecretKey), date)
	kRegion := hmacSHA256(kDate, c.Region)
	kService := hmacSHA256(kRegion, c.Service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKey, scope, signedHeaders, signature))
}

// CanonicalQuery sorts and encodes the query string the way SigV4 wants (a bare key
// becomes key=).
func CanonicalQuery(raw string) string {
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
		pairs = append(pairs, [2]string{URIEncode(ku), URIEncode(vu)})
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

// URIEncodePath encodes every segment of a path.
func URIEncodePath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = URIEncode(s)
	}
	return strings.Join(segs, "/")
}

// URIEncode is RFC 3986 encoding with the unreserved set AWS uses.
func URIEncode(s string) string {
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

// SHA256Hex is the hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
