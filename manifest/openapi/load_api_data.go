// Copyright IBM Corp. 2017, 2026
// SPDX-License-Identifier: MPL-2.0

package openapi

import (
	"embed"
	"fmt"
)

//go:embed assets/v2.json
var openApiV2File embed.FS

// LoadV2data returns the OpenAPI v2 (Swagger) specification embedded in the
// provider binary. The snapshot is taken from Kubernetes v1.37.2 and serves
// as an offline fallback for API endpoints that do not serve /openapi/v2,
// such as CCE clusters accessed through the Huawei Cloud API gateway.
// Kubernetes API compatibility policy only ever adds fields between minor
// versions, so a newer snapshot is a safe superset for older clusters.
func LoadV2data() ([]byte, error) {
	fileContent, err := openApiV2File.ReadFile("assets/v2.json")
	if err != nil {
		return nil, fmt.Errorf("error reading assets/v2.json: %v", err)
	}
	return fileContent, nil
}
