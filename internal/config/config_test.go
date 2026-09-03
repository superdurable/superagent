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
		len(config.HTTP.AllowedOrigins) != 0 ||
		config.Dex.WorkerTarget != defaultWorkerBindAddress ||
		config.BlobCache.MaxBytes != defaultBlobCacheMaxBytes {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func TestLoadNormalizesAllowedOrigins(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		string(EnvHTTPAllowedOrigins): "https://APP.Example.com:443/, http://localhost:3000",
	}
	config, err := load(func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Origin{"https://app.example.com", "http://localhost:3000"}
	if len(config.HTTP.AllowedOrigins) != len(want) {
		t.Fatalf("allowed origins = %q, want %q", config.HTTP.AllowedOrigins, want)
	}
	for index := range want {
		if config.HTTP.AllowedOrigins[index] != want[index] {
			t.Fatalf("allowed origin %d = %q, want %q", index, config.HTTP.AllowedOrigins[index], want[index])
		}
	}
}

func TestLoadRejectsUnsafeAllowedOrigins(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"*",
		"http://app.example.com",
		"https://app.example.com/path",
		"https://user:secret@app.example.com",
		"https://app.example.com,https://app.example.com/",
	} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			_, err := load(func(name string) (string, bool) {
				if name == string(EnvHTTPAllowedOrigins) {
					return value, true
				}
				return "", false
			})
			if err == nil {
				t.Fatal("load error = nil")
			}
		})
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
