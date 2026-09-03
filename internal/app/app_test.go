// Copyright (c) 2022-2026 Super Durable, Inc.
// Licensed under the Apache License, Version 2.0.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/superdurable/superagent/internal/agent"
	"github.com/superdurable/superagent/internal/config"
)

func TestProviderHTTPClientRejectsRedirects(t *testing.T) {
	t.Parallel()
	client, transport := newProviderHTTPClient(time.Second)
	t.Cleanup(transport.CloseIdleConnections)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); err == nil {
		t.Fatal("redirect error = nil")
	}
}

func TestBuildCredentialsUsesProcessDefaults(t *testing.T) {
	t.Parallel()
	store, err := buildCredentials(&config.Providers{
		OpenAI: &config.Provider{APIKey: "openai-key"}, Anthropic: &config.Provider{},
		Gemini: &config.Provider{}, Groq: &config.Provider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !store.HasAPIKey("flow-1", agent.ProviderOpenAI) || store.HasAPIKey("flow-1", agent.ProviderGroq) {
		t.Fatal("credential defaults were not isolated by provider")
	}
}

func TestDialableAddressNormalizesWildcard(t *testing.T) {
	t.Parallel()
	if got := dialableAddress("0.0.0.0:8803"); got != "127.0.0.1:8803" {
		t.Fatalf("dialable address = %q", got)
	}
}
