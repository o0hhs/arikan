package s3backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Config is an S3-compatible bucket: AWS, MinIO, R2, Backblaze or anything else that
// speaks the same API. PathStyle is for endpoints that do not route by a bucket host.
type Config struct {
	Endpoint     string
	Region       string
	Bucket       string
	Prefix       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	PathStyle    bool
}

func (c Config) validate() error {
	switch {
	case strings.TrimSpace(c.Endpoint) == "":
		return errors.New("s3_endpoint")
	case strings.TrimSpace(c.Bucket) == "":
		return errors.New("s3_bucket")
	case strings.TrimSpace(c.AccessKey) == "":
		return errors.New("s3_access_key")
	case strings.TrimSpace(c.SecretKey) == "":
		return errors.New("s3_secret_key")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	return nil
}

type Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag,omitempty"`
	LastModified time.Time `json:"last_modified"`
}

type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time
}

func NewClient(cfg Config, hc *http.Client, now func() time.Time) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Minute}
	}
	if now == nil {
		now = time.Now
	}
	return &Client{cfg: cfg, http: hc, now: now}, nil
}

func (c *Client) Config() Config { return c.cfg }

// PutFile uploads a file with a SHA-256 payload signature.
func (c *Client) PutFile(ctx context.Context, key, path, contentType string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	payload := hex.EncodeToString(h.Sum(nil))
	headers := map[string]string{"content-type": contentType}
	req, err := c.request(ctx, http.MethodPut, key, nil, headers, payload, fi.Size())
	if err != nil {
		return "", err
	}
	req.Body = io.NopCloser(f)
	req.ContentLength = fi.Size()
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := s3Error(resp); err != nil {
		return "", err
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`), nil
}

// GetFile downloads an object into path (which must not exist).
func (c *Client) GetFile(ctx context.Context, key, path string) error {
	req, err := c.request(ctx, http.MethodGet, key, nil, nil, "UNSIGNED-PAYLOAD", 0)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := s3Error(resp); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	req, err := c.request(ctx, http.MethodDelete, key, nil, nil, "UNSIGNED-PAYLOAD", 0)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return s3Error(resp)
}

// List returns every object under prefix, following continuation tokens.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"1000"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		req, err := c.request(ctx, http.MethodGet, "", q, nil, "UNSIGNED-PAYLOAD", 0)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		closeErr := resp.Body.Close()
		if err := s3ErrorBody(resp, body); err != nil {
			return nil, err
		}
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		var page listResult
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			t, _ := time.Parse(time.RFC3339, o.LastModified)
			out = append(out, Object{Key: o.Key, Size: o.Size, ETag: strings.Trim(o.ETag, `"`), LastModified: t})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			break
		}
		token = page.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastModified.After(out[j].LastModified) })
	return out, nil
}

type listResult struct {
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		ETag         string `xml:"ETag"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

func (c *Client) request(ctx context.Context, method, key string, query url.Values, headers map[string]string, payload string, size int64) (*http.Request, error) {
	u, err := c.objectURL(key, query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("x-amz-content-sha256", payload)
	req.Header.Set("x-amz-date", amzDate)
	if c.cfg.SessionToken != "" {
		req.Header.Set("x-amz-security-token", c.cfg.SessionToken)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if size > 0 {
		req.ContentLength = size
	}
	canonicalHeaders, signed := canonicalHeaders(req)
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalQuery(req.URL.Query())
	canonicalRequest := strings.Join([]string{method, canonicalURI, canonicalQuery, canonicalHeaders, signed, payload}, "\n")
	scope := day + "/" + c.cfg.Region + "/s3/aws4_request"
	hash := sha256.Sum256([]byte(canonicalRequest))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	keyBytes := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), day)
	keyBytes = hmacSHA256(keyBytes, c.cfg.Region)
	keyBytes = hmacSHA256(keyBytes, "s3")
	keyBytes = hmacSHA256(keyBytes, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(keyBytes, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.cfg.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
	return req, nil
}

func (c *Client) objectURL(key string, query url.Values) (*url.URL, error) {
	ep := c.cfg.Endpoint
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, errors.New("s3_endpoint")
	}
	// Path and RawPath are set together: Path holds the decoded key (what net/http sends
	// after escaping), RawPath the exact bytes SigV4 signs. Setting only Path to an escaped
	// key would escape the percent signs a second time.
	decoded := []string{}
	escaped := []string{}
	if c.cfg.PathStyle {
		decoded = append(decoded, c.cfg.Bucket)
		escaped = append(escaped, url.PathEscape(c.cfg.Bucket))
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
	}
	if key != "" {
		for _, p := range strings.Split(strings.Trim(key, "/"), "/") {
			decoded = append(decoded, p)
			escaped = append(escaped, url.PathEscape(p))
		}
	}
	u.Path = "/" + strings.Join(decoded, "/")
	u.RawPath = "/" + strings.Join(escaped, "/")
	if u.RawPath == u.Path {
		u.RawPath = ""
	}
	if query != nil {
		u.RawQuery = canonicalQuery(query)
	}
	return u, nil
}

func canonicalHeaders(req *http.Request) (string, string) {
	headers := map[string]string{"host": req.URL.Host}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "user-agent" || lk == "content-length" {
			continue
		}
		headers[lk] = strings.Join(vs, ",")
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(strings.Join(strings.Fields(headers[k]), " "))
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(keys, ";")
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(awsEncode(k))
			b.WriteByte('=')
			b.WriteString(awsEncode(v))
		}
	}
	return b.String()
}

func awsEncode(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "+", "%20"), "%7E", "~")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("s3: %s (%d)", e.Code, e.Status)
	}
	return fmt.Sprintf("s3: HTTP %d", e.Status)
}

func s3Error(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return s3ErrorBody(resp, body)
}

func s3ErrorBody(resp *http.Response, body []byte) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	_ = xml.Unmarshal(body, &e)
	return &Error{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
}
