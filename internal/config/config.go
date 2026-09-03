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

// Package config owns environment parsing and validated process configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvironmentVariable identifies one supported process setting.
type EnvironmentVariable string

const (
	EnvHTTPAddress           EnvironmentVariable = "SUPERAGENT_HTTP_ADDRESS"
	EnvDexFlowServiceAddress EnvironmentVariable = "DEX_FLOW_SERVICE_ADDRESS"
	EnvDexWorkerBindAddress  EnvironmentVariable = "DEX_WORKER_BIND_ADDRESS"
	EnvDexWorkerTarget       EnvironmentVariable = "DEX_WORKER_TARGET"
	EnvBlobCacheDirectory    EnvironmentVariable = "DEX_BLOB_CACHE_DIR"
	EnvBlobCacheMaxBytes     EnvironmentVariable = "DEX_BLOB_CACHE_MAX_BYTES"
	EnvMCPConfig             EnvironmentVariable = "DEX_AGENT_MCP_CONFIG"
	EnvOpenAIAPIKey          EnvironmentVariable = "OPENAI_API_KEY"    //nolint:gosec // Environment variable name, not a credential.
	EnvAnthropicAPIKey       EnvironmentVariable = "ANTHROPIC_API_KEY" //nolint:gosec // Environment variable name, not a credential.
	EnvGeminiAPIKey          EnvironmentVariable = "GEMINI_API_KEY"    //nolint:gosec // Environment variable name, not a credential.
	EnvGroqAPIKey            EnvironmentVariable = "GROQ_API_KEY"      //nolint:gosec // Environment variable name, not a credential.
	EnvOpenAIBaseURL         EnvironmentVariable = "OPENAI_BASE_URL"
	EnvAnthropicBaseURL      EnvironmentVariable = "ANTHROPIC_BASE_URL"
	EnvGeminiBaseURL         EnvironmentVariable = "GEMINI_BASE_URL"
	EnvGroqBaseURL           EnvironmentVariable = "GROQ_BASE_URL"
)

const (
	defaultHTTPAddress            = "127.0.0.1:8080"
	defaultFlowServiceAddress     = "127.0.0.1:8801"
	defaultWorkerBindAddress      = "127.0.0.1:8803"
	defaultBlobCacheDirectory     = "/tmp/superagent-blob-cache"
	defaultBlobCacheMaxBytes      = int64(512 << 20)
	defaultHTTPReadHeaderTimeout  = 10 * time.Second
	defaultHTTPIdleTimeout        = 75 * time.Second
	defaultHTTPShutdownTimeout    = 20 * time.Second
	defaultProviderRequestTimeout = 10 * time.Minute
)

// Config is the immutable validated application configuration.
type Config struct {
	HTTP      *HTTP
	Dex       *Dex
	BlobCache *BlobCache
	MCP       *MCP
	Providers *Providers
}

// HTTP configures the public OpenAPI server.
type HTTP struct {
	Address           string
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// Dex configures the FlowService and application Worker.
type Dex struct {
	FlowServiceAddress string
	WorkerBindAddress  string
	WorkerTarget       string
}

// BlobCache configures the disposable local large-value cache.
type BlobCache struct {
	Directory string
	MaxBytes  int64
}

// MCP configures optional trusted MCP discovery.
type MCP struct {
	ConfigPath string
}

// Providers configures credentials and trusted API origins.
type Providers struct {
	OpenAI         *Provider
	Anthropic      *Provider
	Gemini         *Provider
	Groq           *Provider
	RequestTimeout time.Duration
}

// Provider contains one provider's process credential and optional origin override.
type Provider struct {
	APIKey  string
	BaseURL string
}

// Lookup reads one environment variable.
type Lookup func(string) (string, bool)

// Load reads and validates process configuration.
func Load() (*Config, error) {
	return load(os.LookupEnv)
}

func load(lookup Lookup) (*Config, error) {
	if lookup == nil {
		panic("environment lookup is required")
	}
	maxBytes, err := optionalPositiveInt64(lookup, EnvBlobCacheMaxBytes, defaultBlobCacheMaxBytes)
	if err != nil {
		return nil, err
	}
	workerBind := optional(lookup, EnvDexWorkerBindAddress, defaultWorkerBindAddress)
	workerTarget := optional(lookup, EnvDexWorkerTarget, workerBind)
	config := &Config{
		HTTP: &HTTP{
			Address:           optional(lookup, EnvHTTPAddress, defaultHTTPAddress),
			ReadHeaderTimeout: defaultHTTPReadHeaderTimeout,
			IdleTimeout:       defaultHTTPIdleTimeout,
			ShutdownTimeout:   defaultHTTPShutdownTimeout,
		},
		Dex: &Dex{
			FlowServiceAddress: optional(lookup, EnvDexFlowServiceAddress, defaultFlowServiceAddress),
			WorkerBindAddress:  workerBind,
			WorkerTarget:       workerTarget,
		},
		BlobCache: &BlobCache{
			Directory: optional(lookup, EnvBlobCacheDirectory, defaultBlobCacheDirectory),
			MaxBytes:  maxBytes,
		},
		MCP: &MCP{ConfigPath: optional(lookup, EnvMCPConfig, "")},
		Providers: &Providers{
			OpenAI: &Provider{
				APIKey:  optional(lookup, EnvOpenAIAPIKey, ""),
				BaseURL: optional(lookup, EnvOpenAIBaseURL, ""),
			},
			Anthropic: &Provider{
				APIKey:  optional(lookup, EnvAnthropicAPIKey, ""),
				BaseURL: optional(lookup, EnvAnthropicBaseURL, ""),
			},
			Gemini: &Provider{
				APIKey:  optional(lookup, EnvGeminiAPIKey, ""),
				BaseURL: optional(lookup, EnvGeminiBaseURL, ""),
			},
			Groq: &Provider{
				APIKey:  optional(lookup, EnvGroqAPIKey, ""),
				BaseURL: optional(lookup, EnvGroqBaseURL, ""),
			},
			RequestTimeout: defaultProviderRequestTimeout,
		},
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

// Validate rejects unsafe or unusable process configuration.
func (config *Config) Validate() error {
	if config == nil || config.HTTP == nil || config.Dex == nil || config.BlobCache == nil ||
		config.MCP == nil || config.Providers == nil || config.Providers.OpenAI == nil ||
		config.Providers.Anthropic == nil || config.Providers.Gemini == nil || config.Providers.Groq == nil {
		return errors.New("every configuration section is required")
	}
	for _, address := range []struct {
		name    string
		address string
	}{
		{"HTTP address", config.HTTP.Address},
		{"Dex FlowService address", config.Dex.FlowServiceAddress},
		{"Dex Worker bind address", config.Dex.WorkerBindAddress},
		{"Dex Worker target", config.Dex.WorkerTarget},
	} {
		if err := validateAddress(address.address); err != nil {
			return fmt.Errorf("%s: %w", address.name, err)
		}
	}
	if strings.TrimSpace(config.BlobCache.Directory) == "" || config.BlobCache.MaxBytes <= 0 {
		return errors.New("BlobCache directory and positive byte limit are required")
	}
	if config.HTTP.ReadHeaderTimeout <= 0 || config.HTTP.IdleTimeout <= 0 ||
		config.HTTP.ShutdownTimeout <= 0 || config.Providers.RequestTimeout <= 0 {
		return errors.New("HTTP and provider timeouts must be positive")
	}
	for _, provider := range []struct {
		name   string
		config *Provider
	}{
		{"OpenAI", config.Providers.OpenAI},
		{"Anthropic", config.Providers.Anthropic},
		{"Gemini", config.Providers.Gemini},
		{"Groq", config.Providers.Groq},
	} {
		if err := validateProvider(provider.name, provider.config); err != nil {
			return err
		}
	}
	return nil
}

func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return err
	}
	if host == "" {
		return errors.New("host must not be empty")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func validateProvider(name string, provider *Provider) error {
	for _, character := range provider.APIKey {
		if character < 0x20 || character > 0x7e {
			return fmt.Errorf("%s API key contains non-printable characters", name)
		}
	}
	if provider.BaseURL == "" {
		return nil
	}
	parsed, err := url.Parse(provider.BaseURL)
	if err != nil {
		return fmt.Errorf("%s base URL: %w", name, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s base URL must be an absolute HTTPS URL without credentials, query, or fragment", name)
	}
	return nil
}

func optional(lookup Lookup, name EnvironmentVariable, fallback string) string {
	value, found := lookup(string(name))
	if !found || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func optionalPositiveInt64(lookup Lookup, name EnvironmentVariable, fallback int64) (int64, error) {
	value, found := lookup(string(name))
	if !found || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}
