package common

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recorder captures the headers and body of the last request received by an
// HTTP test server. The handler runs on its own goroutine, so access is
// guarded by a mutex.
type recorder struct {
	mu    sync.Mutex
	req   *http.Request
	body  string
	empty bool
}

func (r *recorder) capture(req *http.Request, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.req = req.Clone(req.Context())
	r.body = body
	r.empty = false
}

func (r *recorder) header(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.req == nil {
		return ""
	}
	return r.req.Header.Get(key)
}

func (r *recorder) bodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body
}

// recordingServer starts an HTTP test server that records every request it
// receives.
func recordingServer(t *testing.T) (*httptest.Server, *recorder) {
	t.Helper()

	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %s", err)
		}
		rec.capture(r, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	return srv, rec
}

func TestBuildWrappersNoop(t *testing.T) {
	wt, err := BuildWrappers(&HuaweiCloudCredential{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if wt != nil {
		t.Errorf("expected nil wrapper when no credentials and no headers are set, got %v", wt)
	}
}

func TestBuildWrappersHalfCredentials(t *testing.T) {
	_, err := BuildWrappers(&HuaweiCloudCredential{AccessKey: "AK"}, nil)
	if err == nil {
		t.Fatal("expected an error when only the access key is set")
	}
	if !strings.Contains(err.Error(), AccessKeyConfiguration) || !strings.Contains(err.Error(), SecretKeyConfiguration) {
		t.Errorf("expected error to mention both %s and %s, got: %s", AccessKeyConfiguration, SecretKeyConfiguration, err)
	}
}

func TestHuaweiCloudAuthTransportRoundTrip(t *testing.T) {
	srv, rec := recordingServer(t)

	cred := &HuaweiCloudCredential{
		AccessKey:     "AKTEST",
		SecretKey:     "SKTEST",
		ProjectId:     "0a5f4f0e5f00f2b82fc0c0d7bcb6a000",
		SecurityToken: "token-123",
	}
	wt, err := BuildWrappers(cred, nil)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if wt == nil {
		t.Fatal("expected a non-nil wrapper when credentials are set")
	}

	client := &http.Client{Transport: wt(http.DefaultTransport)}
	resp, err := client.Post(srv.URL+"/apis/apps/v1/deployments", "application/json", strings.NewReader(`{"kind":"Deployment"}`))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	if v := rec.header(ProjectIdHeaderKey); v != "0a5f4f0e5f00f2b82fc0c0d7bcb6a000" {
		t.Errorf("expected %s header to be set, got %q", ProjectIdHeaderKey, v)
	}
	if v := rec.header(SecurityTokenHeaderKey); v != "token-123" {
		t.Errorf("expected %s header to be set, got %q", SecurityTokenHeaderKey, v)
	}
	if v := rec.header("Authorization"); !strings.HasPrefix(v, "SDK-HMAC-SHA256 Access=AKTEST, ") {
		t.Errorf("expected a signed Authorization header, got %q", v)
	}
	if v := rec.header("X-Sdk-Date"); v == "" {
		t.Error("expected X-Sdk-Date header to be set")
	}
}

func TestHuaweiCloudAuthTransportPreservesBody(t *testing.T) {
	srv, rec := recordingServer(t)

	cred := &HuaweiCloudCredential{AccessKey: "AKTEST", SecretKey: "SKTEST"}
	wt, err := BuildWrappers(cred, nil)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	client := &http.Client{Transport: wt(http.DefaultTransport)}
	resp, err := client.Post(srv.URL, "application/json", strings.NewReader(`{"kind":"Pod"}`))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer resp.Body.Close()

	// The signer reads the body to compute the content hash and must restore
	// it so the API server receives the original payload.
	if v := rec.bodyString(); v != `{"kind":"Pod"}` {
		t.Errorf("expected body to be delivered intact, got %q", v)
	}
}

func TestExternalHeaderTransportRoundTrip(t *testing.T) {
	srv, rec := recordingServer(t)

	// External headers only: no credentials, no signing.
	wt, err := BuildWrappers(&HuaweiCloudCredential{}, NewExternalHeaderTransport(map[string]string{
		"X-Custom-Trace": "trace-id-42",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if wt == nil {
		t.Fatal("expected a non-nil wrapper when external headers are set")
	}

	client := &http.Client{Transport: wt(http.DefaultTransport)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer resp.Body.Close()

	if v := rec.header("X-Custom-Trace"); v != "trace-id-42" {
		t.Errorf("expected X-Custom-Trace header to be set, got %q", v)
	}
	// Without credentials the request must not be signed, and any existing
	// Authorization header must be left alone.
	if v := rec.header("Authorization"); v != "" {
		t.Errorf("expected Authorization header to be untouched, got %q", v)
	}
}

func TestBuildWrappersCombined(t *testing.T) {
	srv, rec := recordingServer(t)

	cred := &HuaweiCloudCredential{AccessKey: "AKTEST", SecretKey: "SKTEST"}
	wt, err := BuildWrappers(cred, NewExternalHeaderTransport(map[string]string{
		"X-Custom-Trace": "trace-id-42",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	client := &http.Client{Transport: wt(http.DefaultTransport)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer resp.Body.Close()

	if v := rec.header("X-Custom-Trace"); v != "trace-id-42" {
		t.Errorf("expected X-Custom-Trace header to be set, got %q", v)
	}
	if v := rec.header("Authorization"); !strings.HasPrefix(v, "SDK-HMAC-SHA256 Access=AKTEST, ") {
		t.Errorf("expected a signed Authorization header, got %q", v)
	}
}
