package common

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureLogs redirects the standard logger into a buffer for the duration
// of the test and returns the buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	return &buf
}

// enableDebugLogging turns on the TF_LOG=DEBUG level that gates the
// LogTransport output.
func enableDebugLogging(t *testing.T) {
	t.Helper()
	t.Setenv("TF_LOG", "DEBUG")
}

// roundTripperFunc adapts a function to an http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func cannedResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     make(http.Header),
	}
}

var (
	startIDPattern = regexp.MustCompile(`→ #(\d+) `)
	entryIDPattern = regexp.MustCompile(`API Request/Response \(#(\d+), `)
	shortErrIDPattern = regexp.MustCompile(`API transport error \(#(\d+), `)
)

func extractIDs(t *testing.T, pattern *regexp.Regexp, s string) []string {
	t.Helper()

	matches := pattern.FindAllStringSubmatch(s, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m[1])
	}
	return ids
}

func idSet(t *testing.T, ids []string) map[string]int {
	t.Helper()

	set := make(map[string]int, len(ids))
	for _, id := range ids {
		set[id]++
	}
	return set
}

func TestLogTransportCorrelationUnderConcurrency(t *testing.T) {
	enableDebugLogging(t)
	buf := captureLogs(t)

	const goroutines = 8
	const requestsPerGoroutine = 10

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: NewLogTransport("Test", http.DefaultTransport)}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < requestsPerGoroutine; i++ {
				resp, err := client.Get(srv.URL)
				if err != nil {
					t.Errorf("unexpected request error: %s", err)
					return
				}
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	want := goroutines * requestsPerGoroutine

	// Every request produces exactly one start line and exactly one merged
	// Request/Response entry carrying the same ID.
	startIDs := extractIDs(t, startIDPattern, buf.String())
	entryIDs := extractIDs(t, entryIDPattern, buf.String())

	if len(startIDs) != want {
		t.Fatalf("expected %d start lines, got %d", want, len(startIDs))
	}
	if len(entryIDs) != want {
		t.Fatalf("expected %d Request/Response entries, got %d", want, len(entryIDs))
	}

	for id, n := range idSet(t, startIDs) {
		if n != 1 {
			t.Errorf("start line ID #%s logged %d times, expected exactly once", id, n)
		}
	}
	for id, n := range idSet(t, entryIDs) {
		if n != 1 {
			t.Errorf("Request/Response entry ID #%s logged %d times, expected exactly once", id, n)
		}
		if idSet(t, startIDs)[id] == 0 {
			t.Errorf("Request/Response entry #%s has no matching start line", id)
		}
	}
	if len(idSet(t, entryIDs)) != len(idSet(t, startIDs)) {
		t.Errorf("expected the same unique IDs on start lines and entries, got %d and %d", len(idSet(t, startIDs)), len(idSet(t, entryIDs)))
	}
}

func TestLogTransportIDsUniqueAcrossInstances(t *testing.T) {
	enableDebugLogging(t)
	buf := captureLogs(t)

	next := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(), nil
	})

	// Two instances simulate the SDKv2 and manifest chains, which each build
	// their own LogTransport: their IDs must still never collide, otherwise
	// "#1" in the log would be ambiguous.
	for i := 0; i < 2; i++ {
		lt := NewLogTransport("Test", next)
		req, _ := http.NewRequest(http.MethodGet, "http://example.com/api", nil)
		if _, err := lt.RoundTrip(req); err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
	}

	entryIDs := extractIDs(t, entryIDPattern, buf.String())
	if len(entryIDs) != 2 {
		t.Fatalf("expected 2 Request/Response entries, got %d", len(entryIDs))
	}
	if entryIDs[0] == entryIDs[1] {
		t.Errorf("expected unique IDs across transport instances, both were #%s", entryIDs[0])
	}
}

func TestLogTransportErrorPath(t *testing.T) {
	enableDebugLogging(t)
	buf := captureLogs(t)

	lt := NewLogTransport("Test", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/api", nil)
	_, err := lt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected the transport error to propagate")
	}

	output := buf.String()

	// The failed transaction is a single merged entry: start line and one
	// entry referencing the same ID, with the request attached.
	startIDs := extractIDs(t, startIDPattern, output)
	entryIDs := extractIDs(t, entryIDPattern, output)

	if len(startIDs) != 1 {
		t.Fatalf("expected 1 start line, got %d", len(startIDs))
	}
	if len(entryIDs) != 1 {
		t.Fatalf("expected 1 merged error entry, got %d", len(entryIDs))
	}
	if startIDs[0] != entryIDs[0] {
		t.Errorf("start line ID #%s and error entry ID #%s differ", startIDs[0], entryIDs[0])
	}
	if !strings.Contains(output, "connection refused") {
		t.Error("expected the error entry to contain the transport error message")
	}
	if !strings.Contains(output, "----[ REQUEST ]") {
		t.Error("expected the error entry to include the request dump")
	}
	if strings.Contains(output, "----[ RESPONSE ]") {
		t.Error("expected the error entry to have no response section")
	}
}

func TestLogTransportErrorPathWithoutDebugLogging(t *testing.T) {
	t.Setenv("TF_LOG", "")
	buf := captureLogs(t)

	lt := NewLogTransport("Test", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api", strings.NewReader(`{"kind":"Secret"}`))
	if _, err := lt.RoundTrip(req); err == nil {
		t.Fatal("expected the transport error to propagate")
	}

	output := buf.String()
	if ids := extractIDs(t, shortErrIDPattern, output); len(ids) != 1 {
		t.Fatalf("expected 1 single-line transport error, got %d", len(ids))
	}
	// Without debug logging there are no start lines and no request dumps,
	// so no request content leaks into the error output.
	if strings.Contains(output, `{"kind":"Secret"}`) {
		t.Error("expected no request body in the log output without debug logging")
	}
	if strings.Contains(output, "----[ REQUEST ]") {
		t.Error("expected no request dump without debug logging")
	}
}

func TestLogTransportSilentWithoutDebugLogging(t *testing.T) {
	t.Setenv("TF_LOG", "")
	buf := captureLogs(t)

	lt := NewLogTransport("Test", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(), nil
	}))

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/api", nil)
	if _, err := lt.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	if buf.Len() != 0 {
		t.Errorf("expected no log output without debug logging, got:\n%s", buf.String())
	}
}

func TestLogTransportRedactsSensitiveHeaders(t *testing.T) {
	enableDebugLogging(t)
	buf := captureLogs(t)

	lt := NewLogTransport("Test", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(), nil
	}))

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api", strings.NewReader(`{"kind":"Secret","stringData":{"password":"topsecret"}}`))
	req.Header.Set("Authorization", "SDK-HMAC-SHA256 Access=AKEXAMPLE, Signature=abcdef0123456789")
	req.Header.Set("X-Security-Token", "token-123")
	if _, err := lt.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	output := buf.String()
	for _, secret := range []string{
		"Signature=abcdef0123456789",
		"token-123",
		`"password":"topsecret"`,
	} {
		if strings.Contains(output, secret) {
			t.Errorf("expected %q to be redacted from the log output", secret)
		}
	}
	if !strings.Contains(output, "Authorization: ******") {
		t.Error("expected the Authorization header to be redacted in place")
	}
}

func TestLogTransportMergedEntryStructure(t *testing.T) {
	enableDebugLogging(t)
	buf := captureLogs(t)

	lt := NewLogTransport("Test", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(), nil
	}))

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/api", nil)
	if _, err := lt.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	output := buf.String()

	// The entry carries both sections next to each other, so the full
	// transaction is readable as one block regardless of concurrent traffic.
	if !strings.Contains(output, "----[ REQUEST ]") || !strings.Contains(output, "----[ RESPONSE ]") {
		t.Errorf("expected the merged entry to contain REQUEST and RESPONSE sections, got:\n%s", output)
	}
	if !regexp.MustCompile(`API Request/Response \(#\d+, [0-9.]+(ns|µs|ms|s)\):`).MatchString(output) {
		t.Errorf("expected the entry title to carry the elapsed time, got:\n%s", output)
	}
}

func TestLogTransportStartLineContent(t *testing.T) {
	enableDebugLogging(t)
	buf := captureLogs(t)

	lt := NewLogTransport("Test", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(), nil
	}))

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/apis/apps/v1/deployments?fieldSelector=x", nil)
	if _, err := lt.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	if !regexp.MustCompile(`→ #\d+ POST /apis/apps/v1/deployments\?fieldSelector=x`).MatchString(buf.String()) {
		t.Errorf("expected the start line to carry method and URI, got:\n%s", buf.String())
	}
}

// TestLogTransportStartLineMarksInFlightRequests verifies that a start line
// without a matching entry marks a hung request: the in-flight request below
// blocks in the transport, so only its start line is ever logged.
func TestLogTransportStartLineMarksInFlightRequests(t *testing.T) {
	enableDebugLogging(t)
	buf := captureLogs(t)

	block := make(chan struct{})
	lt := NewLogTransport("Test", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		<-block
		return cannedResponse(), nil
	}))

	done := make(chan error, 1)
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/api", nil)
	go func() {
		_, err := lt.RoundTrip(req)
		done <- err
	}()

	// Wait for the start line to appear while the request is in flight.
	deadline := make(chan struct{})
	go func() {
		for {
			if strings.Contains(buf.String(), "→ #") {
				close(deadline)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-deadline:
	case <-time.After(5 * time.Second):
		t.Fatal("start line was not logged while the request was in flight")
	}

	if strings.Contains(buf.String(), "API Request/Response (") {
		t.Error("expected no Request/Response entry while the request is in flight")
	}

	close(block)
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
}
