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

package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/superdurable/superagent/internal/config"
)

const testApplicationOrigin = config.Origin("https://app.example.com")

func TestCORSHandlerAllowsConfiguredOrigin(t *testing.T) {
	t.Parallel()
	called := false
	handler := newCORSHandler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		writer.WriteHeader(http.StatusAccepted)
	}), testHTTPConfig(), slog.New(slog.DiscardHandler))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", string(testApplicationOrigin))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if !called || response.Code != http.StatusAccepted {
		t.Fatalf("downstream called/status = %t/%d", called, response.Code)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != string(testApplicationOrigin) {
		t.Fatalf("allow origin = %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
	if response.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentialed cross-origin requests must remain disabled")
	}
}

func TestCORSHandlerAnswersValidPreflight(t *testing.T) {
	t.Parallel()
	handler := newCORSHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight reached downstream handler")
	}), testHTTPConfig(), slog.New(slog.DiscardHandler))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/products/ai-agent/start", nil)
	request.Header.Set("Origin", string(testApplicationOrigin))
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", response.Code)
	}
	if response.Header().Get("Access-Control-Allow-Methods") != allowedCORSMethods ||
		response.Header().Get("Access-Control-Allow-Headers") != allowedCORSHeaders {
		t.Fatalf("preflight headers = %#v", response.Header())
	}
}

func TestCORSHandlerRejectsUnconfiguredOrigin(t *testing.T) {
	t.Parallel()
	handler := newCORSHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("rejected request reached downstream handler")
	}), testHTTPConfig(), slog.New(slog.DiscardHandler))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("rejection status = %d", response.Code)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("rejected allow origin = %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSHandlerRejectsUnsupportedPreflight(t *testing.T) {
	t.Parallel()
	handler := newCORSHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("rejected preflight reached downstream handler")
	}), testHTTPConfig(), slog.New(slog.DiscardHandler))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/products/ai-agent/start", nil)
	request.Header.Set("Origin", string(testApplicationOrigin))
	request.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("rejection status = %d", response.Code)
	}
}

func testHTTPConfig() *config.HTTP {
	return &config.HTTP{AllowedOrigins: []config.Origin{testApplicationOrigin}}
}
