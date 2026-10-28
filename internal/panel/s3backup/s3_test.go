package s3backup

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestObjectURLKeepsKeysEscapedOnce(t *testing.T) {
	c, err := NewClient(Config{Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "b", AccessKey: "a", SecretKey: "s", PathStyle: true}, nil, func() time.Time {
		return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := c.request(context.Background(), http.MethodPut, "backups/панель 1%2F.tar.gz.age", nil, nil, "UNSIGNED-PAYLOAD", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := req.URL.EscapedPath(), "/b/backups/%D0%BF%D0%B0%D0%BD%D0%B5%D0%BB%D1%8C%201%252F.tar.gz.age"; got != want {
		t.Fatalf("escaped path: %q, want %q", got, want)
	}
	if got, want := req.URL.Path, "/b/backups/панель 1%2F.tar.gz.age"; got != want {
		t.Fatalf("decoded path: %q, want %q", got, want)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "AWS4-HMAC-SHA256") || req.Header.Get("x-amz-content-sha256") != "UNSIGNED-PAYLOAD" {
		t.Fatalf("signature headers: %q %q", req.Header.Get("Authorization"), req.Header.Get("x-amz-content-sha256"))
	}
}

func TestListReadsErrorBodyBeforeClosing(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != "backups/" {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		return response(http.StatusForbidden, `<Error><Code>AccessDenied</Code><Message>no</Message></Error>`, nil), nil
	})}
	c, err := NewClient(Config{Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "b", AccessKey: "a", SecretKey: "s", PathStyle: true}, hc, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.List(context.Background(), "backups/")
	var se *Error
	if !errors.As(err, &se) || se.Status != http.StatusForbidden || se.Code != "AccessDenied" || se.Message != "no" {
		t.Fatalf("list error: %#v", err)
	}
}

func TestListAndPut(t *testing.T) {
	var gotPath, gotBody string
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			gotPath = r.URL.EscapedPath()
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			return response(http.StatusOK, "", http.Header{"Etag": {`"etag"`}}), nil
		case http.MethodGet:
			return response(http.StatusOK, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>backups/a.tar.gz.age</Key><Size>12</Size><ETag>"e"</ETag><LastModified>2026-01-02T03:04:05Z</LastModified></Contents></ListBucketResult>`, http.Header{"Content-Type": {"application/xml"}}), nil
		default:
			t.Errorf("method: %s", r.Method)
			return response(http.StatusMethodNotAllowed, "", nil), nil
		}
	})}
	c, err := NewClient(Config{Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "b", AccessKey: "a", SecretKey: "s", PathStyle: true}, hc, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "dump")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	etag, err := c.PutFile(context.Background(), "backups/a.tar.gz.age", path, "application/octet-stream")
	if err != nil || etag != "etag" || gotBody != "hello" || gotPath != "/b/backups/a.tar.gz.age" {
		t.Fatalf("put: %q %q %q %q", etag, gotBody, gotPath, err)
	}
	objs, err := c.List(context.Background(), "backups/")
	if err != nil || len(objs) != 1 || objs[0].Key != "backups/a.tar.gz.age" || objs[0].Size != 12 || objs[0].ETag != "e" {
		t.Fatalf("list: %#v %v", objs, err)
	}
}

func TestRetentionOnlyTouchesPanelArchives(t *testing.T) {
	for key, want := range map[string]bool{
		"arikan-panel-20260102-030405-manual.tar.gz.age":         true,
		"backups/arikan-panel-20260102-030405-manual.tar.gz.age": true,
		"arikan-de-1-20260102-030405-daily.tar.gz.age":           true,
		"backups/other.tar.gz.age":                               false,
		"backups/arikan-panel-20260102-030405-manual.sql":        false,
		"backups/arikan-panel.tar.gz.age":                        false,
	} {
		if got := isBackupObject(key); got != want {
			t.Errorf("isBackupObject(%q) = %v, want %v", key, got, want)
		}
	}
}
