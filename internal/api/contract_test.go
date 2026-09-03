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

package api_test

import (
	"os"
	"slices"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

var phaseOnePaths = []string{
	"/healthz",
	"/products/ai-agent/events",
	"/products/ai-agent/messages",
	"/products/ai-agent/plans/execute",
	"/products/ai-agent/portal",
	"/products/ai-agent/start",
	"/products/ai-agent/tool-approvals",
	"/readyz",
}

var deferredPaths = []string{
	"/products/ai-agent/describe",
	"/products/ai-agent/history",
	"/products/ai-agent/message-queue",
	"/products/ai-agent/message-queue/delete",
	"/products/ai-agent/message-queue/steer",
	"/products/ai-agent/snapshot",
	"/products/ai-agent/status",
}

func TestPhaseOneContractContainsOnlyStablePaths(t *testing.T) {
	document := loadDocument(t)
	paths := make([]string, 0, len(document.Paths))
	for path := range document.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if !slices.Equal(paths, phaseOnePaths) {
		t.Fatalf("OpenAPI paths = %q, want %q", paths, phaseOnePaths)
	}
}

func TestPhaseOneContractOmitsDeferredReadAndQueueMutationPaths(t *testing.T) {
	document := loadDocument(t)
	for _, path := range deferredPaths {
		if _, found := document.Paths[path]; found {
			t.Errorf("deferred path %q must not be present in Phase 1", path)
		}
	}
}

type openAPIDocument struct {
	OpenAPI string                    `yaml:"openapi"`
	Paths   map[string]map[string]any `yaml:"paths"`
}

func loadDocument(t *testing.T) openAPIDocument {
	t.Helper()
	contents, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document openAPIDocument
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode OpenAPI document: %v", err)
	}
	if document.OpenAPI != "3.0.3" {
		t.Fatalf("OpenAPI version = %q, want 3.0.3", document.OpenAPI)
	}
	return document
}
