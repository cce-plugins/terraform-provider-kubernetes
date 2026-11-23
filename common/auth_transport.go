package common

import (
	"fmt"
	"log"
	"net/http"

	"k8s.io/client-go/transport"

	"github.com/hashicorp/terraform-provider-kubernetes/common/signer"
)

const (
	AccessKeyConfiguration     = "hw_access_key"
	SecretKeyConfiguration     = "hw_secret_key"
	ProjectIdConfiguration     = "hw_project_id"
	SecurityTokenConfiguration = "hw_security_token"

	ExternalHeadersConfiguration = "external_headers"

	ProjectIdHeaderKey     = "X-Project-Id"
	SecurityTokenHeaderKey = "X-Security-Token"
)

// BuildWrappers composes the Huawei Cloud request-signing, external-header
// and debug-logging transports into a single client-go transport wrapper.
// It returns a nil wrapper when neither signing credentials (both access key
// and secret key must be provided) nor external headers are configured,
// leaving the transport chain untouched so non-Huawei Cloud clusters behave
// exactly as before.
func BuildWrappers(c *HuaweiCloudCredential, externalHeader *ExternalHeaderTransport) (transport.WrapperFunc, error) {
	if err := c.Validation(); err != nil {
		return nil, err
	}

	// NOTE: transport.Wrappers applies the functions in order, so the first
	// entry ends up closest to the wire:
	//
	//	debugLogWrapper  (innermost) dumps the request right before it hits
	//	                  the wire, i.e. after external headers and the
	//	                  Huawei Cloud signature are applied, with sensitive
	//	                  headers redacted.
	//	signing wrapper  signs the request (Authorization, X-Sdk-Date).
	//	external header  (outermost) applies custom headers, which are then
	//	                  included in the signature.
	//
	// The debug transport installed by the SDKv2 and manifest provider
	// configuration remains outermost of the whole chain, so request tracing
	// via TF_LOG produces the upstream log lines in addition to the
	// correlated ones emitted here.
	var wrappers []transport.WrapperFunc
	if c.Enabled() || externalHeader != nil {
		wrappers = append(wrappers, debugLogWrapper)
	}
	if c.Enabled() {
		wrappers = append(wrappers, c.TransportWrapper)
	}
	if externalHeader != nil {
		wrappers = append(wrappers, externalHeader.TransportWrapper)
	}

	return transport.Wrappers(wrappers...), nil
}

// debugLogWrapper wraps rt in a LogTransport that emits correlated
// request/response logs when the log level is DEBUG or higher.
func debugLogWrapper(rt http.RoundTripper) http.RoundTripper {
	return NewLogTransport("Kubernetes", rt)
}

type HuaweiCloudCredential struct {
	AccessKey     string
	SecretKey     string
	ProjectId     string
	SecurityToken string
}

// Enabled reports whether requests should be signed with the Huawei Cloud
// signature, which requires both an access key and a secret key.
func (c *HuaweiCloudCredential) Enabled() bool {
	return len(c.AccessKey) != 0 && len(c.SecretKey) != 0
}

// Validation ensures the credentials are either fully set or fully unset.
// Half-configured credentials are almost certainly a mistake and would
// otherwise silently disable request signing.
func (c *HuaweiCloudCredential) Validation() error {
	if (len(c.AccessKey) == 0) != (len(c.SecretKey) == 0) {
		return fmt.Errorf("both %s and %s must be set to enable Huawei Cloud authentication, or both must be left empty", AccessKeyConfiguration, SecretKeyConfiguration)
	}
	return nil
}

func (c *HuaweiCloudCredential) TransportWrapper(rt http.RoundTripper) http.RoundTripper {
	if err := c.Validation(); err != nil {
		return rt
	}

	log.Printf("[TRACE] use Huawei Cloud Certification transport for request")
	return &HuaweiCloudAuthTransport{
		hcc:  c,
		next: rt,
	}
}

type HuaweiCloudAuthTransport struct {
	hcc  *HuaweiCloudCredential
	next http.RoundTripper
}

func (d *HuaweiCloudAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(d.hcc.ProjectId) != 0 {
		req.Header.Add(ProjectIdHeaderKey, d.hcc.ProjectId)
	}
	if len(d.hcc.SecurityToken) != 0 {
		req.Header.Add(SecurityTokenHeaderKey, d.hcc.SecurityToken)
	}

	hs := &signer.Signer{
		Key:    d.hcc.AccessKey,
		Secret: d.hcc.SecretKey,
	}
	if err := hs.Sign(req); err != nil {
		log.Printf("[ERROR] error signing request: %s", err)
		return nil, err
	}

	return d.next.RoundTrip(req)
}

type ExternalHeaderTransport struct {
	headers map[string]string
	next    http.RoundTripper
}

func NewExternalHeaderTransport(headers map[string]string) *ExternalHeaderTransport {
	if len(headers) == 0 {
		return nil
	}
	return &ExternalHeaderTransport{headers: headers}
}

func (e *ExternalHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for key := range e.headers {
		req.Header.Set(key, e.headers[key])
	}
	return e.next.RoundTrip(req)
}

func (e *ExternalHeaderTransport) TransportWrapper(rt http.RoundTripper) http.RoundTripper {
	if len(e.headers) == 0 {
		return rt
	}
	e.next = rt
	// NOTE: the original implementation returned `rt` here, which installed
	// this transport's next hop but never put it into the chain, so external
	// headers were silently dropped. Returning `e` actually applies them.
	return e
}
