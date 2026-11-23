package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/go-hclog"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/hashicorp/terraform-provider-kubernetes/manifest/openapi"
)

// notFoundHandler responds to every request with a Kubernetes-style 404,
// simulating the Huawei Cloud CCE API gateway which does not serve /openapi/v2.
func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"the server could not find the requested resource","reason":"NotFound","details":{},"code":404}`))
}

// specHandler responds to /openapi/v2 with the given spec.
func specHandler(spec string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(spec))
	}
}

// countingServer starts a test server that counts the requests it receives.
func countingServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv, &hits
}

// minimalSpec only defines ObjectMeta: it is deliberately NOT the embedded
// (full) Kubernetes spec, so a lookup of Pod succeeding or failing tells
// which source the foundry was built from.
const minimalSpec = `{"swagger":"2.0","info":{"title":"Kubernetes","version":"v1.0.0"},"paths":{},"definitions":{"io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta":{"type":"object"}}}`

var podGVK = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}

func newTestRawProviderServer(t *testing.T, srv *httptest.Server) *RawProviderServer {
	t.Helper()

	t.Setenv("KUBE_OPEN_API_V2", "")
	cfg := &rest.Config{Host: srv.URL}
	// Mirror the provider configuration: a raw REST client refuses to be
	// built without a negotiated serializer.
	codec := runtime.NoopEncoder{Decoder: scheme.Codecs.UniversalDecoder()}
	cfg.NegotiatedSerializer = serializer.NegotiatedSerializerWrapper(runtime.SerializerInfo{Serializer: codec})
	return &RawProviderServer{
		logger:       hclog.NewNullLogger(),
		clientConfig: cfg,
	}
}

func TestGetOAPIv2FoundryFallsBackToEmbeddedSpecOn404(t *testing.T) {
	// The CCE API gateway answers 404 for /openapi/v2: the provider must
	// fall back to the embedded spec instead of failing.
	srv, hits := countingServer(t, notFoundHandler)
	s := newTestRawProviderServer(t, srv)

	foundry, err := s.getOAPIv2Foundry()
	if err != nil {
		t.Fatalf("expected the embedded spec fallback, got: %s", err)
	}

	if got := hits.Load(); got != 1 {
		t.Errorf("expected exactly 1 request to the API server, got %d", got)
	}

	// The embedded spec is the full Kubernetes spec: ObjectMeta must resolve.
	if _, _, err := foundry.GetTypeByGVK(openapi.ObjectMetaGVK); err != nil {
		t.Errorf("expected ObjectMeta in the embedded spec, got: %s", err)
	}

	// The foundry is cached: a second lookup must not hit the server again.
	if _, err := s.getOAPIv2Foundry(); err != nil {
		t.Fatalf("unexpected error on second call: %s", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected the foundry to be cached, server was hit %d times", got)
	}
}

func TestGetOAPIv2FoundryFallsBackWhenServerUnreachable(t *testing.T) {
	srv, _ := countingServer(t, notFoundHandler)
	s := newTestRawProviderServer(t, srv)
	srv.Close() // connection refused from now on

	foundry, err := s.getOAPIv2Foundry()
	if err != nil {
		t.Fatalf("expected the embedded spec fallback, got: %s", err)
	}
	if _, _, err := foundry.GetTypeByGVK(openapi.ObjectMetaGVK); err != nil {
		t.Errorf("expected ObjectMeta in the embedded spec, got: %s", err)
	}
}

func TestGetOAPIv2FoundryFromServer(t *testing.T) {
	srv, hits := countingServer(t, specHandler(minimalSpec))
	s := newTestRawProviderServer(t, srv)

	foundry, err := s.getOAPIv2Foundry()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected exactly 1 request to the API server, got %d", got)
	}

	// Pod is not part of the minimal spec served above, but is part of the
	// embedded spec: the lookup must fail, proving the server spec was used.
	if _, _, err := foundry.GetTypeByGVK(podGVK); err == nil {
		t.Error("expected Pod to be missing from the served (minimal) spec")
	}
}

func TestGetOAPIv2FoundryServerOnly(t *testing.T) {
	srv, _ := countingServer(t, notFoundHandler)
	s := newTestRawProviderServer(t, srv)
	t.Setenv("KUBE_OPEN_API_V2", "server")

	_, err := s.getOAPIv2Foundry()
	if err == nil {
		t.Fatal("expected an error when the server does not serve /openapi/v2 and KUBE_OPEN_API_V2=server")
	}
	if !strings.Contains(err.Error(), "failed get OpenAPI spec") {
		t.Errorf("expected the raw server error to surface, got: %s", err)
	}
}

func TestGetOAPIv2FoundryEmbeddedOnly(t *testing.T) {
	srv, hits := countingServer(t, notFoundHandler)
	s := newTestRawProviderServer(t, srv)
	t.Setenv("KUBE_OPEN_API_V2", "embedded")

	foundry, err := s.getOAPIv2Foundry()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("expected no request to the API server, got %d", got)
	}
	if _, _, err := foundry.GetTypeByGVK(openapi.ObjectMetaGVK); err != nil {
		t.Errorf("expected ObjectMeta in the embedded spec, got: %s", err)
	}
}

func TestGetOAPIv2FoundryFromFile(t *testing.T) {
	srv, hits := countingServer(t, notFoundHandler)
	s := newTestRawProviderServer(t, srv)

	specPath := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(specPath, []byte(minimalSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBE_OPEN_API_V2", specPath)

	foundry, err := s.getOAPIv2Foundry()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("expected no request to the API server, got %d", got)
	}

	// Pod is only in the embedded spec, not in the file above.
	if _, _, err := foundry.GetTypeByGVK(podGVK); err == nil {
		t.Error("expected Pod to be missing from the file-based (minimal) spec")
	}
}
