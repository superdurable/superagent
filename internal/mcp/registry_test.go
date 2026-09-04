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
	"log/slog"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegisteredToolDefaultsWritesToOneAttemptAndApproval(t *testing.T) {
	registered, err := registeredTool(ServerConfig{
		Name:      "docs",
		Transport: TransportStdio,
		Command:   "server",
		Tools:     map[string]ToolPolicy{},
	}, &mcpsdk.Tool{
		Name:        "create-document",
		Description: "Create a document",
		InputSchema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("registeredTool() error = %v", err)
	}
	if !registered.Definition.RequiresApproval || registered.Definition.MaximumAttempts != 1 {
		t.Fatalf("unsafe defaults = %+v", registered.Definition)
	}
}

func TestRegisteredToolTrustsReadOnlyAnnotationOnlyWhenConfigured(t *testing.T) {
	readOnly := &mcpsdk.Tool{
		Name:        "search",
		InputSchema: map[string]any{"type": "object"},
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}
	server := ServerConfig{Name: "web", Transport: TransportStdio, Command: "server", Tools: map[string]ToolPolicy{}}
	untrusted, err := registeredTool(server, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	server.TrustReadOnlyAnnotations = true
	trusted, err := registeredTool(server, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !untrusted.Definition.RequiresApproval || trusted.Definition.RequiresApproval || trusted.Definition.MaximumAttempts != 3 {
		t.Fatalf("untrusted = %+v, trusted = %+v", untrusted.Definition, trusted.Definition)
	}
}

func TestComponentNameIsStableAndProviderSafe(t *testing.T) {
	short := componentName("google-docs", "create/document")
	if short != "google_docs__create_document" {
		t.Fatalf("componentName() = %q", short)
	}
	long := componentName(strings.Repeat("server", 10), strings.Repeat("tool", 10))
	if len(long) != 64 || long != componentName(strings.Repeat("server", 10), strings.Repeat("tool", 10)) {
		t.Fatalf("long component name = %q (%d)", long, len(long))
	}
}

func TestDefinitionsIncludeBrokersOnlyWhenServersExist(t *testing.T) {
	empty, err := NewRegistry(nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if got := empty.Definitions(nil, nil); len(got) != 0 {
		t.Fatalf("empty definitions = %+v", got)
	}
	if got := empty.ServerNames(); got == nil {
		t.Fatal("empty server names must be an initialized slice")
	}
	configured, err := NewRegistry([]ServerConfig{{Name: "one", Transport: TransportStdio, Command: "server"}}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if got := configured.Definitions(nil, nil); len(got) != len(brokerNames) {
		t.Fatalf("configured definitions count = %d", len(got))
	}
}

func TestNewRegistryRequiresLogger(t *testing.T) {
	deferred := func() {
		if recover() == nil {
			t.Fatal("NewRegistry() did not panic for a missing required logger")
		}
	}
	defer deferred()
	_, _ = NewRegistry(nil, nil)
}

func TestNewRegistryOwnsNestedConfiguration(t *testing.T) {
	readOnly := true
	servers := []ServerConfig{{
		Name:        "docs",
		Transport:   TransportStdio,
		Command:     "server",
		Args:        []string{"before"},
		Environment: map[string]string{"TOKEN": "SOURCE_TOKEN"},
		Tools: map[string]ToolPolicy{
			"read": {ReadOnly: &readOnly},
		},
	}}
	registry, err := NewRegistry(servers, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	servers[0].Args[0] = "after"
	servers[0].Environment["TOKEN"] = "CHANGED_TOKEN"
	policy := servers[0].Tools["read"]
	*policy.ReadOnly = false
	servers[0].Tools["read"] = policy
	stored := registry.servers["docs"]
	if stored.Args[0] != "before" || stored.Environment["TOKEN"] != "SOURCE_TOKEN" || !*stored.Tools["read"].ReadOnly {
		t.Fatalf("registry configuration was mutated through caller-owned memory: %+v", stored)
	}
}

func TestEmptyRegistryCanRestartAfterClose(t *testing.T) {
	registry, err := NewRegistry(nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	registry.Close()
	if err := registry.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestTransportValidationReturnsTypedError(t *testing.T) {
	_, err := NewRegistry([]ServerConfig{{Name: "bad", Transport: Transport("socket")}}, slog.Default())
	var validationErr *TransportValidationError
	if !errors.As(err, &validationErr) || validationErr.Value != "socket" {
		t.Fatalf("NewRegistry() error = %v", err)
	}
}
