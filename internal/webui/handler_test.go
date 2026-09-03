// Copyright (c) 2022-2026 Super Durable, Inc.
// Licensed under the Apache License, Version 2.0.
// SPDX-License-Identifier: Apache-2.0

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesEmbeddedApplication(t *testing.T) {
	t.Parallel()
	api := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
	})
	handler := NewHandler(api)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequestWithContext(context.Background(), http.MethodGet, pagePath, nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "id=\"root\"") {
		t.Fatalf("page status/body = %d/%q", page.Code, page.Body.String())
	}
	if page.Header().Get("Content-Security-Policy") == "" || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page headers = %#v", page.Header())
	}

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequestWithContext(context.Background(), http.MethodGet, assetPath+string(assetBundle), nil))
	if asset.Code != http.StatusOK || asset.Header().Get("ETag") == "" || asset.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("asset status/headers = %d/%#v", asset.Code, asset.Header())
	}
}

func TestHandlerDelegatesAPIAndRejectsUIWrites(t *testing.T) {
	t.Parallel()
	api := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
	})
	handler := NewHandler(api)

	delegated := httptest.NewRecorder()
	handler.ServeHTTP(delegated, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil))
	if delegated.Code != http.StatusTeapot {
		t.Fatalf("delegated status = %d", delegated.Code)
	}

	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequestWithContext(context.Background(), http.MethodPost, pagePath, nil))
	if rejected.Code != http.StatusMethodNotAllowed || rejected.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("rejected status/headers = %d/%#v", rejected.Code, rejected.Header())
	}
}
