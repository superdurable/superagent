// Copyright (c) 2022-2026 Super Durable, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package mcpregistry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigAppliesSafeDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	contents := []byte(`servers:
  - name: search
    transport: stdio
    command: search-server
    tools:
      query:
        read_only: true
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	servers, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	policy := servers[0].Tools["query"]
	if policy.TimeoutSeconds != 60 || policy.RetryTotalSeconds != 300 {
		t.Fatalf("policy defaults = %+v", policy)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, `servers:
  - name: search
    transport: stdio
    command: search-server
    typo: rejected
`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig() error = nil")
	}
}

func TestLoadConfigRejectsMultipleDocuments(t *testing.T) {
	path := writeConfig(t, `servers: []
---
servers: []
`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig() error = nil")
	}
}

func TestLoadConfigReturnsTypedTransportError(t *testing.T) {
	path := writeConfig(t, `servers:
  - name: search
    transport: websocket
`)
	_, err := LoadConfig(path)
	var validationErr *TransportValidationError
	if !errors.As(err, &validationErr) || validationErr.Value != "websocket" {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}

func TestLoadConfigRejectsAmbiguousHTTPConfiguration(t *testing.T) {
	tests := map[string]string{
		"credentials in URL": `servers:
  - name: remote
    transport: streamable_http
    url: https://user:secret@example.test/mcp
`,
		"stdio field": `servers:
  - name: remote
    transport: streamable_http
    url: https://example.test/mcp
    command: ignored
`,
		"host header": `servers:
  - name: remote
    transport: streamable_http
    url: https://example.test/mcp
    headers:
      Host: MCP_HOST
`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, contents)); err == nil {
				t.Fatal("LoadConfig() error = nil")
			}
		})
	}
}

func TestLoadConfigRejectsUnsafeRetryPolicy(t *testing.T) {
	path := writeConfig(t, `servers:
  - name: search
    transport: stdio
    command: search-server
    tools:
      query:
        timeout_seconds: .nan
`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig() error = nil")
	}
}

func TestResolveEnvironmentRequiresEveryConfiguredSource(t *testing.T) {
	const missing = "SUPERAGENT_TEST_MISSING_MCP_SECRET"
	t.Setenv(missing, "present")
	if err := os.Unsetenv(missing); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveEnvironment(map[string]string{"TOKEN": missing}); err == nil {
		t.Fatal("resolveEnvironment() error = nil")
	}
}

func TestLoadConfigRejectsDuplicateServerNames(t *testing.T) {
	path := writeConfig(t, `servers:
  - name: duplicate
    transport: stdio
    command: first
  - name: duplicate
    transport: stdio
    command: second
`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig() error = nil")
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
