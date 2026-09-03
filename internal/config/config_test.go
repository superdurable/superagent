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

package config

import "testing"

func TestLoadUsesValidatedDefaults(t *testing.T) {
	t.Parallel()
	config, err := load(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if config.HTTP.Address != defaultHTTPAddress ||
		config.Dex.WorkerTarget != defaultWorkerBindAddress ||
		config.BlobCache.MaxBytes != defaultBlobCacheMaxBytes {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func TestLoadRejectsInvalidEnvironment(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		string(EnvHTTPAddress):       "missing-port",
		string(EnvBlobCacheMaxBytes): "-1",
	}
	_, err := load(func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	})
	if err == nil {
		t.Fatal("load error = nil")
	}
}

func TestConfigRejectsCredentialedProviderOrigin(t *testing.T) {
	t.Parallel()
	values := map[string]string{string(EnvOpenAIBaseURL): "https://secret@example.com/v1"}
	_, err := load(func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	})
	if err == nil {
		t.Fatal("load error = nil")
	}
}
