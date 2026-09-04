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

// Package app owns the SuperAgent process dependency graph and lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/superdurable/dex/blob-cache-go/blobcache"
	"github.com/superdurable/dex/sdk-go/dex"
	"github.com/superdurable/superagent/internal/agent"
	httpapi "github.com/superdurable/superagent/internal/api"
	"github.com/superdurable/superagent/internal/config"
	mcpregistry "github.com/superdurable/superagent/internal/mcp"
	"github.com/superdurable/superagent/internal/model"
)

const (
	workerStartupTimeout = 2 * time.Minute
	workerProbeInterval  = 25 * time.Millisecond
	maximumHeaderBytes   = 1 << 20
)

// Run constructs, serves, cancels, and joins the complete process lifecycle.
func Run(ctx context.Context, applicationConfig *config.Config, logger *slog.Logger) error {
	if ctx == nil || applicationConfig == nil || logger == nil {
		panic("application context, config, and logger are required")
	}
	if err := applicationConfig.Validate(); err != nil {
		return fmt.Errorf("validate application config: %w", err)
	}
	owned, err := build(ctx, applicationConfig, logger)
	if err != nil {
		return err
	}
	return owned.run(ctx)
}

type ownedApplication struct {
	config        *config.Config
	logger        *slog.Logger
	worker        *dex.Worker
	dexClient     *dex.Client
	cache         *blobcache.Cache
	mcp           *mcpregistry.Registry
	providerRound *http.Transport
	httpServer    *http.Server
	ready         atomic.Bool
}

func build(ctx context.Context, applicationConfig *config.Config, logger *slog.Logger) (_ *ownedApplication, returnedErr error) {
	owned := &ownedApplication{config: applicationConfig, logger: logger}
	defer func() {
		if returnedErr != nil {
			returnedErr = errors.Join(returnedErr, owned.close())
		}
	}()

	toolRegistry, err := buildToolRegistry(applicationConfig.MCP, logger)
	if err != nil {
		return nil, err
	}
	owned.mcp = toolRegistry
	if startErr := toolRegistry.Start(ctx); startErr != nil {
		return nil, fmt.Errorf("start MCP registry: %w", startErr)
	}

	credentials, err := buildCredentials(applicationConfig.Providers)
	if err != nil {
		return nil, err
	}
	providerHTTP, providerRound := newProviderHTTPClient(applicationConfig.Providers.RequestTimeout)
	owned.providerRound = providerRound
	modelClient, err := buildModelClient(applicationConfig.Providers, credentials, providerHTTP)
	if err != nil {
		return nil, err
	}
	flow := agent.NewFlow(modelClient, toolRegistry)
	dexRegistry, err := dex.NewRegistry([]dex.Flow{flow})
	if err != nil {
		return nil, fmt.Errorf("register Agent Flow: %w", err)
	}
	cache, err := blobcache.New(&blobcache.Config{
		Dir:      applicationConfig.BlobCache.Directory,
		MaxBytes: applicationConfig.BlobCache.MaxBytes,
		Logger:   logger,
	})
	if err != nil {
		return nil, fmt.Errorf("open BlobCache: %w", err)
	}
	owned.cache = cache
	if addressErr := ensureAddressAvailable(ctx, applicationConfig.Dex.WorkerBindAddress); addressErr != nil {
		return nil, fmt.Errorf("preflight Dex Worker address: %w", addressErr)
	}
	worker, err := dex.NewWorker(dexRegistry, cache, dex.WorkerOptions{
		BindAddress:        applicationConfig.Dex.WorkerBindAddress,
		FlowServiceAddress: applicationConfig.Dex.FlowServiceAddress,
		WorkerTarget: dex.WorkerTarget{
			Address: applicationConfig.Dex.WorkerTarget,
		},
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Dex Worker: %w", err)
	}
	owned.worker = worker
	dexClient, err := dex.NewClient(dexRegistry, cache, dex.ClientOptions{
		FlowServiceAddress: applicationConfig.Dex.FlowServiceAddress,
		WorkerTarget:       worker.WorkerTarget(),
		Logger:             logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Dex Client: %w", err)
	}
	owned.dexClient = dexClient
	agentClient := agent.NewClient(dexClient, flow)
	apiHandler := httpapi.NewHandler(agentClient, toolRegistry, credentials, owned.ready.Load, logger)
	generatedHandler, err := httpapi.NewHTTPHandler(apiHandler, applicationConfig.HTTP, logger)
	if err != nil {
		return nil, fmt.Errorf("construct OpenAPI server: %w", err)
	}
	owned.httpServer = &http.Server{
		Addr:              applicationConfig.HTTP.Address,
		Handler:           generatedHandler,
		ReadHeaderTimeout: applicationConfig.HTTP.ReadHeaderTimeout,
		IdleTimeout:       applicationConfig.HTTP.IdleTimeout,
		MaxHeaderBytes:    maximumHeaderBytes,
	}
	return owned, nil
}

func (application *ownedApplication) run(ctx context.Context) error {
	workerErr := make(chan error, 1)
	go func() {
		workerErr <- application.worker.Start()
	}()
	workerFinished, err := waitForWorker(ctx, application.config.Dex.WorkerBindAddress, workerErr)
	if err != nil {
		shutdownErr := application.shutdownServing(ctx)
		var workerJoinErr error
		if !workerFinished {
			workerJoinErr = <-workerErr
		}
		return errors.Join(err, shutdownErr, workerJoinErr, application.close())
	}
	application.ready.Store(true)
	serverErr := make(chan error, 1)
	go func() {
		err := application.httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
	}()

	var runErr error
	serverFinished := false
	select {
	case <-ctx.Done():
	case err := <-workerErr:
		workerFinished = true
		if err == nil {
			runErr = errors.New("dex worker stopped unexpectedly")
		} else {
			runErr = fmt.Errorf("dex worker stopped: %w", err)
		}
	case err := <-serverErr:
		serverFinished = true
		if err == nil {
			runErr = errors.New("HTTP server stopped unexpectedly")
		} else {
			runErr = fmt.Errorf("HTTP server stopped: %w", err)
		}
	}
	application.ready.Store(false)
	shutdownErr := application.shutdownServing(ctx)
	var workerJoinErr error
	if !workerFinished {
		workerJoinErr = <-workerErr
	}
	var serverJoinErr error
	if !serverFinished {
		serverJoinErr = <-serverErr
	}
	return errors.Join(runErr, shutdownErr, workerJoinErr, serverJoinErr, application.close())
}

func (application *ownedApplication) shutdownServing(ctx context.Context) error {
	shutdownContext := context.WithoutCancel(ctx)
	httpCtx, cancelHTTP := context.WithTimeout(shutdownContext, application.config.HTTP.ShutdownTimeout)
	httpErr := application.httpServer.Shutdown(httpCtx)
	cancelHTTP()
	workerCtx, cancelWorker := context.WithTimeout(shutdownContext, application.config.HTTP.ShutdownTimeout)
	workerErr := application.worker.Stop(workerCtx)
	cancelWorker()
	return errors.Join(httpErr, workerErr)
}

func (application *ownedApplication) close() error {
	application.ready.Store(false)
	var errs []error
	if application.dexClient != nil {
		errs = append(errs, application.dexClient.Close())
		application.dexClient = nil
	}
	if application.cache != nil {
		errs = append(errs, application.cache.Close())
		application.cache = nil
	}
	if application.mcp != nil {
		application.mcp.Close()
		application.mcp = nil
	}
	if application.providerRound != nil {
		application.providerRound.CloseIdleConnections()
		application.providerRound = nil
	}
	return errors.Join(errs...)
}

func ensureAddressAvailable(ctx context.Context, address string) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return listener.Close()
}

func buildToolRegistry(section *config.MCP, logger *slog.Logger) (*mcpregistry.Registry, error) {
	if section.ConfigPath == "" {
		return mcpregistry.NewRegistry(nil, logger)
	}
	registry, err := mcpregistry.NewRegistryFromFile(section.ConfigPath, logger)
	if err != nil {
		return nil, fmt.Errorf("load MCP registry: %w", err)
	}
	return registry, nil
}

func buildCredentials(section *config.Providers) (*model.CredentialStore, error) {
	store := model.NewCredentialStore()
	for _, provider := range []struct {
		name   agent.Provider
		config *config.Provider
	}{
		{agent.ProviderOpenAI, section.OpenAI},
		{agent.ProviderAnthropic, section.Anthropic},
		{agent.ProviderGemini, section.Gemini},
		{agent.ProviderGroq, section.Groq},
	} {
		if err := store.SetDefaultAPIKey(provider.name, provider.config.APIKey); err != nil {
			return nil, fmt.Errorf("configure %s credential: %w", provider.name, err)
		}
	}
	return store, nil
}

func buildModelClient(section *config.Providers, credentials *model.CredentialStore, httpClient *http.Client) (*model.Client, error) {
	anthropic, err := model.NewAnthropicClient(credentials, httpClient, section.Anthropic.BaseURL)
	if err != nil {
		return nil, err
	}
	gemini, err := model.NewGeminiClient(credentials, httpClient, section.Gemini.BaseURL)
	if err != nil {
		return nil, err
	}
	groq, err := model.NewGroqClient(credentials, httpClient, section.Groq.BaseURL)
	if err != nil {
		return nil, err
	}
	return model.NewClient(
		model.NewMockClient(),
		model.NewOpenAIClient(credentials, httpClient, section.OpenAI.BaseURL),
		anthropic,
		gemini,
		groq,
	), nil
}

func newProviderHTTPClient(timeout time.Duration) (*http.Client, *http.Transport) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("provider redirects are disabled")
		},
	}, transport
}

func waitForWorker(ctx context.Context, address string, workerErr <-chan error) (bool, error) {
	startupCtx, cancel := context.WithTimeout(ctx, workerStartupTimeout)
	defer cancel()
	dialAddress := dialableAddress(address)
	ticker := time.NewTicker(workerProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-workerErr:
			if err == nil {
				return true, errors.New("dex worker stopped during startup")
			}
			return true, fmt.Errorf("start Dex Worker: %w", err)
		case <-startupCtx.Done():
			return false, fmt.Errorf("wait for Dex Worker: %w", startupCtx.Err())
		case <-ticker.C:
			connection, err := (&net.Dialer{Timeout: workerProbeInterval}).DialContext(startupCtx, "tcp", dialAddress)
			if err == nil {
				_ = connection.Close()
				select {
				case workerStartErr := <-workerErr:
					if workerStartErr != nil {
						return true, fmt.Errorf("start Dex Worker: %w", workerStartErr)
					}
					return true, errors.New("dex worker stopped during startup")
				default:
					return false, nil
				}
			}
		}
	}
}

func dialableAddress(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
