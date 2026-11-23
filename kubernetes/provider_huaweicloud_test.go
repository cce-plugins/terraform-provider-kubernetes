package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"github.com/hashicorp/terraform-provider-kubernetes/common"
)

// The Huawei Cloud authentication attributes must be purely additive: when
// they are not configured, the provider has to behave exactly as upstream,
// i.e. no transport wrapper is installed on the client config. These tests
// pin that contract so future changes cannot silently alter the transport
// chain for non-CCE clusters.

// testProviderConfigure runs providerConfigure with the given raw provider
// configuration and environment overrides. Environment variables that feed
// provider defaults are neutralized first so the tests are deterministic;
// anything in envs is applied afterwards.
func testProviderConfigure(t *testing.T, raw map[string]interface{}, envs map[string]string) (providerMetadata, diag.Diagnostics) {
	t.Helper()

	for _, env := range []string{
		"HW_ACCESS_KEY", "HW_SECRET_KEY", "HW_PROJECT_ID", "HW_SECURITY_TOKEN",
		"KUBE_CONFIG_PATH", "KUBE_CONFIG_PATHS", "KUBE_HOST", "TF_LOG",
	} {
		t.Setenv(env, "")
	}
	for k, v := range envs {
		t.Setenv(k, v)
	}

	p := Provider()
	d := schema.TestResourceDataRaw(t, p.Schema, raw)
	meta, diags := providerConfigure(context.Background(), d, "test")
	if diags.HasError() {
		return providerMetadata{}, diags
	}

	pm, ok := meta.(providerMetadata)
	if !ok {
		t.Fatalf("expected providerConfigure to return providerMetadata, got %T", meta)
	}
	if pm.config == nil {
		t.Fatal("expected providerConfigure to produce a client config")
	}
	return pm, diags
}

func TestProviderConfigureWithoutHuaweiCloudCredentials(t *testing.T) {
	// No Huawei Cloud attributes, no external headers: the transport chain
	// must be left untouched (WrapTransport == nil), exactly as upstream.
	pm, diags := testProviderConfigure(t, map[string]interface{}{}, nil)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if pm.config.WrapTransport != nil {
		t.Errorf("expected no transport wrapper without Huawei Cloud credentials, got %v", pm.config.WrapTransport)
	}
}

func TestProviderConfigureWithHuaweiCloudCredentials(t *testing.T) {
	pm, diags := testProviderConfigure(t, map[string]interface{}{
		"hw_access_key": "AKTEST",
		"hw_secret_key": "SKTEST",
	}, nil)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if pm.config.WrapTransport == nil {
		t.Error("expected a transport wrapper when Huawei Cloud credentials are set")
	}
}

func TestProviderConfigureWithHuaweiCloudCredentialsFromEnv(t *testing.T) {
	pm, diags := testProviderConfigure(t, map[string]interface{}{}, map[string]string{
		"HW_ACCESS_KEY": "AKTEST",
		"HW_SECRET_KEY": "SKTEST",
	})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if pm.config.WrapTransport == nil {
		t.Error("expected a transport wrapper when Huawei Cloud credentials are set via environment")
	}
}

func TestProviderConfigureWithHalfHuaweiCloudCredentials(t *testing.T) {
	// Only the access key: a half-configured credential must surface a
	// clear configuration error rather than silently disabling signing.
	_, diags := testProviderConfigure(t, map[string]interface{}{
		"hw_access_key": "AKTEST",
	}, nil)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic when only hw_access_key is set")
	}
	if got := fmt.Sprintf("%+v", diags); !strings.Contains(got, common.AccessKeyConfiguration) || !strings.Contains(got, common.SecretKeyConfiguration) {
		t.Errorf("expected the error to mention both credentials, got: %+v", diags)
	}
}

func TestProviderConfigureWithExternalHeadersOnly(t *testing.T) {
	// External headers work on their own, without credentials: headers are
	// applied but requests are not signed.
	pm, diags := testProviderConfigure(t, map[string]interface{}{
		"external_headers": map[string]interface{}{
			"X-Custom-Trace": "trace-id-42",
		},
	}, nil)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if pm.config.WrapTransport == nil {
		t.Error("expected a transport wrapper when external headers are set")
	}
}

func TestProviderConfigureDebugTransportStillInstalled(t *testing.T) {
	// With debug logging enabled and no Huawei Cloud configuration, the
	// upstream debug transport must still be installed — the new code path
	// must not swallow it.
	pm, diags := testProviderConfigure(t, map[string]interface{}{}, map[string]string{
		"TF_LOG": "DEBUG",
	})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if pm.config.WrapTransport == nil {
		t.Error("expected the debug transport to be installed when TF_LOG is DEBUG")
	}
}
