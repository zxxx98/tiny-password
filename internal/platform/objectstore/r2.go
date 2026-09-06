// Package objectstore implements the S3-compatible object storage adapter
// used for R2 backup delivery (design §11.2, T22). It speaks the minimal
// AWS SigV4-signed REST subset Cloudflare R2 supports — no SDK dependency
// (decision 0002, checked 2026-09-06 against developers.cloudflare.com/r2:
// endpoint https://<account>.r2.cloudflarestorage.com, region "auto",
// PutObject/CopyObject/ListObjectsV2 supported).
//
// Credentials are caller-supplied (Docker Secret files); they never appear
// in logs or errors. Integrity never relies on ETags (R2 ETags are not
// SHA-256 digests): callers verify content by reading bytes back.
package objectstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Config describes one R2 (S3-compatible) target. Endpoint is the account
// endpoint without bucket; Region is "auto" for R2.
type Config struct {
	Endpoint        string // https://<account>.r2.cloudflarestorage.com
	Region          string // "auto"
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	// HTTPTimeout bounds one request; zero uses 60s.
	HTTPTimeout time.Duration
}

// Client is a minimal S3 REST client.
type Client struct {
	cfg  Config
	http *http.Client
	// Now and sleep are injectable for retry tests.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	// MaxAttempts bounds retries; zero uses 4.
	MaxAttempts int
}

// NewClient validates the configuration.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("objectstore: endpoint, bucket and credentials are required")
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        4,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
		now:         time.Now,
		sleep:       contextualSleep,
		MaxAttempts: 4,
	}, nil
}

// Stable errors. Permanent wraps 4xx responses (except 429): retrying
// cannot help, the run fails immediately.
var (
	ErrNotFound    = errors.New("objectstore: object not found")
	ErrPermanent   = errors.New("objectstore: permanent error")
	ErrUnavailable = errors.New("objectstore: service unavailable")
	ErrCanceled    = errors.New("objectstore: canceled")
	ErrUnexpected  = errors.New("objectstore: unexpected response")
)

// ObjectExists is reported by Head.
type HeadInfo struct {
	Size         int64
	ETag         string
	LastModified time.Time
}

func (c *Client) endpointURL(path string, query url.Values) *url.URL {
	u, err := url.Parse(strings.TrimSuffix(c.cfg.Endpoint, "/") + path)
	if err != nil {
		// NewClient validated the endpoint; this guards programmer error.
		panic(fmt.Sprintf("objectstore: invalid endpoint: %v", err))
	}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	return u
}

// signedRequest builds and signs one request. payloadHash is the hex
// SHA-256 of the body (or the literal "UNSIGNED-PAYLOAD"). contentLength
// below zero means "no body" (GET/HEAD/DELETE/copy).
func (c *Client) signedRequest(ctx context.Context, method string, u *url.URL, body io.Reader, contentLength int64, payloadHash string, extraHeaders map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if contentLength >= 0 {
		// Setting req.ContentLength is the only reliable way; a raw
		// Content-Length header is dropped by net/http.
		req.ContentLength = contentLength
	}
	amzDate := c.now().UTC().Format("20060102T150405Z")
	dateStamp := amzDate[:8]
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("Host", u.Host)

	// Canonical headers: lowercase, sorted, trimmed values.
	names := make([]string, 0, len(req.Header))
	for k := range req.Header {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, name := range names {
		canonHeaders.WriteString(name)
		canonHeaders.WriteString(":")
		canonHeaders.WriteString(strings.TrimSpace(req.Header.Get(http.CanonicalHeaderKey(name))))
		canonHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonicalURI := u.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		u.RawQuery,
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + c.cfg.Region + "/s3/aws4_request"
	hashedCanonical := sha256Hex([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hashedCanonical}, "\n")
	// AWS SigV4 key derivation chain (kDate -> kRegion -> kService -> kSigning).
	kDate := hmacSHA256([]byte("AWS4"+c.cfg.SecretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(c.cfg.Region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKeyID, scope, signedHeaders, signature,
	))
	return req, nil
}

// bodyProvider returns a fresh body for one request attempt plus its
// length (or nil for body-less requests). The HTTP transport closes the
// returned reader after every attempt, so retries must rebuild it.
type bodyProvider func() (io.Reader, int64, error)

// do executes a signed request with bounded retries on transient failures.
// Permanent (4xx except 429) errors fail immediately.
func (c *Client) do(ctx context.Context, method string, u *url.URL, payloadHash string, extraHeaders map[string]string, body bodyProvider) (*http.Response, error) {
	maxAttempts := c.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 4
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var bodyReader io.Reader
		var length int64 = -1
		if body != nil {
			reader, n, err := body()
			if err != nil {
				return nil, fmt.Errorf("%w: open body: %v", ErrUnexpected, err)
			}
			if reader != nil {
				bodyReader = reader
				length = n
			}
		}
		req, err := c.signedRequest(ctx, method, u, bodyReader, length, payloadHash, extraHeaders)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			// Transport-level failure: transient (network, timeout).
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("%w: %v", ErrCanceled, ctxErr)
			}
			lastErr = fmt.Errorf("%w: transport: %v", ErrUnavailable, err)
		} else if retryableStatus(resp.StatusCode) {
			drainClose(resp)
			lastErr = fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
		} else {
			return resp, nil
		}
		if attempt < maxAttempts {
			backoff := time.Duration(250<<uint(attempt-1)) * time.Millisecond
			backoff += time.Duration(rand.Int63n(int64(backoff / 4)))
			if err := c.sleep(ctx, backoff); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
			}
		}
	}
	return nil, lastErr
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == 500 || code == 502 || code == 503 || code == 504
}

func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

// statusError turns an error response into a typed error.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	drainClose(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: credentials rejected (HTTP %d)", ErrPermanent, resp.StatusCode)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return fmt.Errorf("%w: HTTP %d", ErrPermanent, resp.StatusCode)
	default:
		return fmt.Errorf("%w: HTTP %d: %s", ErrUnexpected, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// PutObject uploads r (known size and SHA-256) to key. The digest rides the
// SigV4 signed payload, so the wire integrity is authenticated end to end.
// The body is re-opened per attempt, so r must be a factory-backed reader
// (a *os.File path via NewFileBody or equivalent).
func (c *Client) PutObject(ctx context.Context, key string, openBody func() (io.ReadCloser, error), size int64, payloadSHA256Hex string) error {
	u := c.endpointURL("/"+c.cfg.Bucket+"/"+escapeKey(key), nil)
	body := func() (io.Reader, int64, error) {
		f, err := openBody()
		if err != nil {
			return nil, 0, err
		}
		return f, size, nil
	}
	resp, err := c.do(ctx, http.MethodPut, u, payloadSHA256Hex, nil, body)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	return nil
}

// GetObject streams the object body; the caller owns closing.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	u := c.endpointURL("/"+c.cfg.Bucket+"/"+escapeKey(key), nil)
	resp, err := c.do(ctx, http.MethodGet, u, emptySHA256, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	return resp.Body, nil
}

// HeadObject reports existence and size (never used as integrity proof).
func (c *Client) HeadObject(ctx context.Context, key string) (HeadInfo, error) {
	u := c.endpointURL("/"+c.cfg.Bucket+"/"+escapeKey(key), nil)
	resp, err := c.do(ctx, http.MethodHead, u, emptySHA256, nil, nil)
	if err != nil {
		return HeadInfo{}, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return HeadInfo{}, statusError(resp)
	}
	info := HeadInfo{Size: resp.ContentLength, ETag: resp.Header.Get("ETag")}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			info.LastModified = t
		}
	}
	return info, nil
}

// DeleteObject removes one object.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	u := c.endpointURL("/"+c.cfg.Bucket+"/"+escapeKey(key), nil)
	resp, err := c.do(ctx, http.MethodDelete, u, emptySHA256, nil, nil)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return statusError(resp)
}

// CopyObject performs a server-side copy from srcKey to dstKey.
func (c *Client) CopyObject(ctx context.Context, srcKey, dstKey string) error {
	u := c.endpointURL("/"+c.cfg.Bucket+"/"+escapeKey(dstKey), nil)
	copySource := "/" + c.cfg.Bucket + "/" + escapeKey(srcKey)
	resp, err := c.do(ctx, http.MethodPut, u, emptySHA256, map[string]string{
		"x-amz-copy-source": copySource,
	}, nil)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	// S3 reports copy failures inside a 200 response body.
	var result struct {
		Error *struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err := xml.Unmarshal(raw, &result); err == nil && result.Error != nil {
		return fmt.Errorf("%w: copy rejected: %s", ErrUnexpected, result.Error.Code)
	}
	return nil
}

// Object is one listing entry.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// ListObjects lists up to 1000 keys under prefix.
func (c *Client) ListObjects(ctx context.Context, prefix string) ([]Object, error) {
	query := url.Values{"list-type": {"2"}, "prefix": {prefix}}
	u := c.endpointURL("/"+c.cfg.Bucket, query)
	resp, err := c.do(ctx, http.MethodGet, u, emptySHA256, nil, nil)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	var listing struct {
		Contents []struct {
			Key          string    `xml:"Key"`
			Size         int64     `xml:"Size"`
			LastModified time.Time `xml:"LastModified"`
		} `xml:"Contents"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: list read: %v", ErrUnexpected, err)
	}
	if err := xml.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("%w: list parse: %v", ErrUnexpected, err)
	}
	objects := make([]Object, 0, len(listing.Contents))
	for _, c := range listing.Contents {
		objects = append(objects, Object{Key: c.Key, Size: c.Size, LastModified: c.LastModified})
	}
	return objects, nil
}

func escapeKey(key string) string {
	// Keep "/" separators; escape each segment.
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var emptySHA256 = sha256Hex(nil)

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func contextualSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
