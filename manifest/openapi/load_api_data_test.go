// Copyright IBM Corp. 2017, 2026
// SPDX-License-Identifier: MPL-2.0

package openapi

import (
	"strings"
	"testing"
)

// TestLoadV2data verifies the spec embedded in the provider binary is present
// and parses into a working foundry. This is the offline fallback for API
// endpoints that do not serve /openapi/v2 (e.g. the Huawei Cloud CCE API
// gateway), so a broken asset must never go unnoticed.
func TestLoadV2data(t *testing.T) {
	b, err := LoadV2data()
	if err != nil {
		t.Fatalf("unexpected error loading the embedded spec: %s", err)
	}
	if !strings.Contains(string(b), `"swagger":"2.0"`) {
		t.Error("expected the embedded spec to be a swagger 2.0 document")
	}
	if !strings.Contains(string(b), `io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta`) {
		t.Error("expected the embedded spec to define ObjectMeta")
	}

	foundry, err := NewFoundryFromSpecV2(b)
	if err != nil {
		t.Fatalf("failed to construct foundry from the embedded spec: %s", err)
	}
	if _, _, err := foundry.GetTypeByGVK(ObjectMetaGVK); err != nil {
		t.Errorf("expected ObjectMeta to resolve in the embedded spec: %s", err)
	}
}
