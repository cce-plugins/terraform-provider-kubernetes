package mux

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// TestMuxServerProviderSchemaParity asserts that the three muxed provider
// servers (the SDKv2 kubernetes provider, the SDKv2 manifest provider and the
// Plugin Framework provider) expose identical provider configuration
// schemas. tf6muxserver merges the schemas returned by every server and
// reports an error diagnostic when they differ, so any drift — for example
// adding an attribute to only one of the three schemas — fails this test.
func TestMuxServerProviderSchemaParity(t *testing.T) {
	ctx := context.Background()

	server, err := MuxServer(ctx, "test")
	if err != nil {
		t.Fatalf("unexpected error creating mux server: %s", err)
	}

	resp, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("unexpected error calling GetProviderSchema: %s", err)
	}

	for _, diag := range resp.Diagnostics {
		if diag.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("provider schema mismatch across muxed servers: %s: %s", diag.Summary, diag.Detail)
		}
	}

	if resp.Provider == nil || resp.Provider.Block == nil {
		t.Fatal("expected a provider schema to be returned")
	}

	// The Huawei Cloud CCE authentication attributes must be declared by
	// every muxed server for the parity above to hold.
	found := make(map[string]bool)
	for _, attr := range resp.Provider.Block.Attributes {
		found[attr.Name] = true
	}
	for _, name := range []string{
		"hw_access_key", "hw_secret_key", "hw_project_id", "hw_security_token", "external_headers",
	} {
		if !found[name] {
			t.Errorf("expected attribute %q in the provider schema", name)
		}
	}
}
