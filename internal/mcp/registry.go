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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/superdurable/superagent/internal/agent"
)

const (
	defaultToolTimeout    = 60 * time.Second
	defaultRetryDuration  = 5 * time.Minute
	maximumRetryBackoff   = 5 * time.Second
	maximumPublicNameSize = 64
)

var invalidNameCharacter = regexp.MustCompile(`[^A-Za-z0-9_]`)

const toolFailureStatusFailed toolFailureStatus = "failed"

type toolFailureStatus string

type toolFailure struct {
	Status    toolFailureStatus `json:"status"`
	Attempts  int               `json:"attempts"`
	Outcome   agent.ToolOutcome `json:"outcome"`
	ErrorType string            `json:"error_type"`
}

type registryLifecycle uint8

const (
	registryStopped registryLifecycle = iota
	registryStarting
	registryReady
	registryClosing
)

// Registry discovers and executes tools from trusted MCP server configuration.
type Registry struct {
	mutex         sync.RWMutex
	servers       map[string]ServerConfig
	serverNames   []string
	lifecycle     registryLifecycle
	startDone     chan struct{}
	startCancel   context.CancelFunc
	startErr      error
	tools         map[agent.ToolName]agent.RegisteredTool
	logger        *slog.Logger
	httpTransport *http.Transport
}

// NewRegistry validates immutable server configuration.
func NewRegistry(servers []ServerConfig, logger *slog.Logger) (*Registry, error) {
	if logger == nil {
		panic("MCP registry logger is required")
	}
	serverMap := make(map[string]ServerConfig, len(servers))
	serverNames := make([]string, 0, len(servers))
	for index := range servers {
		server := cloneServerConfig(servers[index])
		applyDefaults(&server)
		if err := validateServer(server); err != nil {
			return nil, fmt.Errorf("MCP server %d: %w", index, err)
		}
		if _, found := serverMap[server.Name]; found {
			return nil, errors.New("MCP server names must be unique")
		}
		serverMap[server.Name] = server
		serverNames = append(serverNames, server.Name)
	}
	slices.Sort(serverNames)
	return &Registry{
		servers:       serverMap,
		serverNames:   serverNames,
		tools:         make(map[agent.ToolName]agent.RegisteredTool),
		logger:        logger,
		httpTransport: newHTTPTransport(),
	}, nil
}

// NewRegistryFromFile loads a trusted YAML file and validates every server.
func NewRegistryFromFile(path string, logger *slog.Logger) (*Registry, error) {
	servers, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return NewRegistry(servers, logger)
}

// Start connects to each server once and atomically publishes discovered tools.
func (registry *Registry) Start(ctx context.Context) error {
	registry.mutex.Lock()
	switch registry.lifecycle {
	case registryReady:
		registry.mutex.Unlock()
		return nil
	case registryStarting:
		done := registry.startDone
		registry.mutex.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			registry.mutex.RLock()
			defer registry.mutex.RUnlock()
			return registry.startErr
		}
	case registryStopped:
		// This caller performs discovery; concurrent callers join startDone.
	case registryClosing:
		done := registry.startDone
		registry.mutex.Unlock()
		if done != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
			}
		}
		return errors.New("MCP registry is closing")
	default:
		registry.mutex.Unlock()
		return errors.New("MCP registry has an invalid lifecycle state")
	}
	discoveryCtx, cancel := context.WithCancel(ctx)
	registry.lifecycle = registryStarting
	registry.startDone = make(chan struct{})
	registry.startCancel = cancel
	registry.startErr = nil
	registry.mutex.Unlock()

	discovered := make(map[agent.ToolName]agent.RegisteredTool)
	var discoveryErr error
	for _, serverName := range registry.serverNames {
		server := registry.servers[serverName]
		if err := registry.discoverServer(discoveryCtx, server, discovered); err != nil {
			discoveryErr = fmt.Errorf("discover MCP server %q: %w", serverName, err)
			break
		}
	}
	cancel()

	registry.mutex.Lock()
	switch {
	case registry.lifecycle == registryClosing:
		registry.tools = make(map[agent.ToolName]agent.RegisteredTool)
		registry.lifecycle = registryStopped
	case discoveryErr == nil:
		registry.tools = discovered
		registry.lifecycle = registryReady
	default:
		registry.tools = make(map[agent.ToolName]agent.RegisteredTool)
		registry.lifecycle = registryStopped
	}
	registry.startErr = discoveryErr
	registry.startCancel = nil
	close(registry.startDone)
	registry.mutex.Unlock()
	return discoveryErr
}

// Close cancels and joins discovery, then removes disposable discovered state.
func (registry *Registry) Close() {
	registry.mutex.Lock()
	if registry.lifecycle == registryStarting {
		registry.lifecycle = registryClosing
		done := registry.startDone
		cancel := registry.startCancel
		registry.mutex.Unlock()
		cancel()
		<-done
		registry.mutex.Lock()
	}
	registry.lifecycle = registryStopped
	registry.tools = make(map[agent.ToolName]agent.RegisteredTool)
	registry.mutex.Unlock()
	registry.httpTransport.CloseIdleConnections()
}

// ServerNames returns configured server names in stable order.
func (registry *Registry) ServerNames() []string {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	return append([]string(nil), registry.serverNames...)
}

// RegisteredTools returns immutable discovered tool projections in stable order.
func (registry *Registry) RegisteredTools() []agent.RegisteredTool {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	result := make([]agent.RegisteredTool, 0, len(registry.tools))
	for _, tool := range registry.tools {
		result = append(result, tool)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Definition.Name < result[right].Definition.Name
	})
	return result
}

// Definitions filters model-visible tools by enabled servers and names.
func (registry *Registry) Definitions(enabledServers []string, enabledTools []agent.ToolName) []agent.ToolDefinition {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	servers := stringSet(enabledServers)
	if len(servers) == 0 {
		servers = stringSet(registry.serverNames)
	}
	names := toolNameSet(enabledTools)
	result := make([]agent.ToolDefinition, 0, len(registry.tools)+len(brokerNames))
	for _, tool := range registry.tools {
		if _, enabled := servers[tool.ServerName]; !enabled {
			continue
		}
		if len(names) > 0 {
			if _, enabled := names[tool.Definition.Name]; !enabled {
				continue
			}
		}
		result = append(result, tool.Definition)
	}
	if len(servers) > 0 {
		for _, definition := range brokerDefinitions() {
			if len(names) == 0 {
				result = append(result, definition)
				continue
			}
			if _, enabled := names[definition.Name]; enabled {
				result = append(result, definition)
			}
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result
}

// Execute invokes an enabled MCP tool using bounded, safety-aware retries.
func (registry *Registry) Execute(
	ctx context.Context,
	invocation agent.ToolInvocation,
) (agent.ToolExecutionResult, error) {
	if invocation.WriteProgress == nil {
		return agent.ToolExecutionResult{}, errors.New("MCP progress writer is required")
	}
	registry.mutex.RLock()
	servers := make(map[string]ServerConfig, len(registry.servers))
	for serverName, server := range registry.servers {
		servers[serverName] = server
	}
	registered, found := registry.tools[invocation.Name]
	isStarted := registry.lifecycle == registryReady
	registry.mutex.RUnlock()
	allowedServers := stringSet(invocation.EnabledServers)
	if len(allowedServers) == 0 {
		for serverName := range servers {
			allowedServers[serverName] = struct{}{}
		}
	}
	if _, broker := brokerNames[invocation.Name]; broker {
		return registry.executeBroker(ctx, invocation, allowedServers, servers)
	}
	if !isStarted {
		return agent.ToolExecutionResult{}, errors.New("MCP registry is not started")
	}
	if !found {
		return agent.ToolExecutionResult{}, fmt.Errorf("unknown MCP tool %q", invocation.Name)
	}
	if _, allowed := allowedServers[registered.ServerName]; !allowed {
		return agent.ToolExecutionResult{}, fmt.Errorf("MCP server %q is not enabled", registered.ServerName)
	}
	return registry.executeWithRetry(ctx, registered, servers[registered.ServerName], invocation)
}

func (registry *Registry) discoverServer(
	ctx context.Context,
	server ServerConfig,
	discovered map[agent.ToolName]agent.RegisteredTool,
) error {
	session, err := registry.connect(ctx, server, nil)
	if err != nil {
		return err
	}
	var operationErr error
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			registry.logger.WarnContext(ctx, "close MCP discovery session", "server", server.Name, "error", closeErr)
		}
	}()
	cursor := ""
	for {
		result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			operationErr = err
			break
		}
		for _, tool := range result.Tools {
			if tool == nil {
				operationErr = errors.New("MCP tools/list returned a nil tool")
				break
			}
			registered, err := registeredTool(server, tool)
			if err != nil {
				operationErr = err
				break
			}
			if _, duplicate := discovered[registered.Definition.Name]; duplicate {
				operationErr = fmt.Errorf("duplicate normalized MCP tool name %q", registered.Definition.Name)
				break
			}
			discovered[registered.Definition.Name] = registered
		}
		if operationErr != nil || result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return operationErr
}

func (registry *Registry) executeWithRetry(
	ctx context.Context,
	tool agent.RegisteredTool,
	server ServerConfig,
	invocation agent.ToolInvocation,
) (agent.ToolExecutionResult, error) {
	definition := tool.Definition
	deadline := time.Now().Add(definition.RetryTotalDuration)
	var lastErr error
	attempts := 0
	for attempts < definition.MaximumAttempts && !time.Now().After(deadline) {
		attempts++
		if err := invocation.WriteProgress(fmt.Sprintf("Calling %s (attempt %d).", definition.Name, attempts)); err != nil {
			return agent.ToolExecutionResult{}, err
		}
		attemptTimeout := definition.AttemptTimeout
		if remaining := time.Until(deadline); remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		result, err := registry.executeOnce(attemptCtx, cancel, tool, server, invocation)
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempts >= definition.MaximumAttempts || time.Now().After(deadline) {
			break
		}
		backoff := time.Second << (attempts - 1)
		if backoff > maximumRetryBackoff {
			backoff = maximumRetryBackoff
		}
		if remaining := time.Until(deadline); remaining < backoff {
			backoff = remaining
		}
		if err := waitForRetry(ctx, backoff); err != nil {
			return agent.ToolExecutionResult{}, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("MCP retry deadline expired before the first attempt")
	}
	registry.logger.WarnContext(ctx, "MCP tool failed", "tool", definition.Name, "attempts", attempts, "error_type", errorTypeName(lastErr))
	outcome := agent.ToolOutcomeUnknown
	if !definition.RequiresApproval {
		outcome = agent.ToolOutcomeKnownFailure
	}
	encoded, err := json.Marshal(toolFailure{
		Status:    toolFailureStatusFailed,
		Attempts:  attempts,
		Outcome:   outcome,
		ErrorType: errorTypeName(lastErr),
	})
	if err != nil {
		return agent.ToolExecutionResult{}, err
	}
	return agent.ToolExecutionResult{Content: string(encoded), Outcome: outcome, IsError: true}, nil
}

func (registry *Registry) executeOnce(
	ctx context.Context,
	cancel context.CancelFunc,
	tool agent.RegisteredTool,
	server ServerConfig,
	invocation agent.ToolInvocation,
) (agent.ToolExecutionResult, error) {
	progress := newProgressCapture(invocation.WriteProgress, cancel)
	session, err := registry.connect(ctx, server, progress)
	if err != nil {
		return agent.ToolExecutionResult{}, err
	}
	arguments, err := decodeMCPArguments(invocation.Arguments)
	if err != nil {
		closeErr := session.Close()
		return agent.ToolExecutionResult{}, errors.Join(err, closeErr)
	}
	params := &mcpsdk.CallToolParams{Name: tool.RemoteName, Arguments: arguments}
	params.SetProgressToken(string(invocation.CallID))
	result, callErr := session.CallTool(ctx, params)
	closeErr := session.Close()
	if callErr != nil {
		return agent.ToolExecutionResult{}, errors.Join(callErr, closeErr, progress.Err())
	}
	if progressErr := progress.Err(); progressErr != nil {
		return agent.ToolExecutionResult{}, errors.Join(progressErr, closeErr)
	}
	if closeErr != nil {
		registry.logger.WarnContext(ctx, "close MCP tool session after result", "server", server.Name, "error", closeErr)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return agent.ToolExecutionResult{}, fmt.Errorf("encode MCP tool result: %w", err)
	}
	outcome := agent.ToolOutcomeSucceeded
	if result.IsError {
		outcome = agent.ToolOutcomeKnownFailure
	}
	return agent.ToolExecutionResult{Content: string(encoded), Outcome: outcome, IsError: result.IsError}, nil
}

func (registry *Registry) connect(
	ctx context.Context,
	server ServerConfig,
	progress *progressCapture,
) (*mcpsdk.ClientSession, error) {
	options := &mcpsdk.ClientOptions{
		Capabilities: &mcpsdk.ClientCapabilities{},
		Logger:       slog.New(mcpSDKLogHandler{next: registry.logger.Handler()}),
		LoggingMessageHandler: func(logContext context.Context, request *mcpsdk.LoggingMessageRequest) {
			registry.logger.InfoContext(logContext, "MCP server log received", "server", server.Name, "level", request.Params.Level)
		},
	}
	if progress != nil {
		options.ProgressNotificationHandler = func(_ context.Context, request *mcpsdk.ProgressNotificationClientRequest) {
			progress.Write(request.Params.Progress, request.Params.Total, request.Params.Message)
		}
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "superagent", Version: "0.1.0"}, options)
	var transport mcpsdk.Transport
	switch server.Transport {
	case TransportStdio:
		resolved, err := resolveEnvironment(server.Environment)
		if err != nil {
			return nil, err
		}
		// Command and arguments come only from validated operator configuration.
		command := exec.CommandContext(ctx, server.Command, server.Args...) //nolint:gosec
		command.Dir = server.CWD
		command.Env = minimalEnvironment(resolved)
		transport = &mcpsdk.CommandTransport{Command: command}
	case TransportStreamableHTTP:
		resolved, err := resolveEnvironment(server.Headers)
		if err != nil {
			return nil, err
		}
		transport = &mcpsdk.StreamableClientTransport{
			Endpoint:             server.URL,
			HTTPClient:           registry.mcpHTTPClient(resolved),
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", server.Transport)
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect MCP server %q: %w", server.Name, err)
	}
	if progress != nil && progress.Err() != nil {
		return nil, errors.Join(progress.Err(), session.Close())
	}
	return session, nil
}

func registeredTool(server ServerConfig, tool *mcpsdk.Tool) (agent.RegisteredTool, error) {
	publicName := componentName(server.Name, tool.Name)
	policy, configured := server.Tools[tool.Name]
	if !configured {
		policy = ToolPolicy{TimeoutSeconds: 60, RetryTotalSeconds: 300}
	}
	readOnly := policy.ReadOnly
	if readOnly == nil && server.TrustReadOnlyAnnotations && tool.Annotations != nil {
		value := tool.Annotations.ReadOnlyHint
		readOnly = &value
	}
	maximumAttempts := 1
	if readOnly != nil && *readOnly {
		maximumAttempts = 3
	}
	if policy.MaximumAttempts != nil {
		maximumAttempts = *policy.MaximumAttempts
	}
	attemptTimeout := time.Duration(policy.TimeoutSeconds * float64(time.Second))
	if attemptTimeout == 0 {
		attemptTimeout = defaultToolTimeout
	}
	retryDuration := time.Duration(policy.RetryTotalSeconds * float64(time.Second))
	if retryDuration == 0 {
		retryDuration = defaultRetryDuration
	}
	if maximumAttempts <= 0 || attemptTimeout <= 0 || retryDuration <= 0 {
		return agent.RegisteredTool{}, fmt.Errorf("invalid policy for %q", publicName)
	}
	inputSchema, err := schemaObject(tool.InputSchema)
	if err != nil {
		return agent.RegisteredTool{}, fmt.Errorf("tool %q input schema: %w", publicName, err)
	}
	description := tool.Description
	if description == "" {
		description = "MCP tool " + tool.Name
	}
	return agent.RegisteredTool{
		ServerName: server.Name,
		RemoteName: tool.Name,
		Definition: agent.ToolDefinition{
			Name:               publicName,
			Description:        description,
			InputSchema:        inputSchema,
			RequiresApproval:   readOnly == nil || !*readOnly,
			AttemptTimeout:     attemptTimeout,
			MaximumAttempts:    maximumAttempts,
			RetryTotalDuration: retryDuration,
		},
	}, nil
}

func schemaObject(value any) (agent.JSONObject, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return agent.ParseJSONObject(string(encoded))
}

func componentName(serverName string, component string) agent.ToolName {
	normalizedServer := invalidNameCharacter.ReplaceAllString(serverName, "_")
	normalizedComponent := invalidNameCharacter.ReplaceAllString(component, "_")
	candidate := normalizedServer + "__" + normalizedComponent
	if len(candidate) <= maximumPublicNameSize {
		return agent.ToolName(candidate)
	}
	digest := sha256.Sum256([]byte(candidate))
	return agent.ToolName(candidate[:55] + "_" + hex.EncodeToString(digest[:])[:8])
}

func minimalEnvironment(additional map[string]string) []string {
	environment := make(map[string]string, len(additional)+3)
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value, found := os.LookupEnv(name); found {
			environment[name] = value
		}
	}
	for name, value := range additional {
		environment[name] = value
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	slices.Sort(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+environment[name])
	}
	return result
}

func newHTTPTransport() *http.Transport {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		panic("net/http default transport has an unexpected type")
	}
	transport := defaultTransport.Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return transport
}

func (registry *Registry) mcpHTTPClient(headers map[string]string) *http.Client {
	return &http.Client{Transport: headerTransport{next: registry.httpTransport, headers: headers}}
}

type headerTransport struct {
	next    http.RoundTripper
	headers map[string]string
}

func (transport headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header = request.Header.Clone()
	for name, value := range transport.headers {
		request.Header.Set(name, value)
	}
	return transport.next.RoundTrip(request)
}

type progressCapture struct {
	mutex  sync.Mutex
	write  agent.TextWriter
	cancel context.CancelFunc
	err    error
}

func newProgressCapture(write agent.TextWriter, cancel context.CancelFunc) *progressCapture {
	return &progressCapture{write: write, cancel: cancel}
}

func (progress *progressCapture) Write(current float64, total float64, message string) {
	detail := message
	if detail == "" {
		detail = fmt.Sprintf("MCP progress: %v", current)
	}
	if total != 0 {
		detail = fmt.Sprintf("%s / %v", detail, total)
	}
	if err := progress.write(detail); err != nil {
		progress.mutex.Lock()
		if progress.err == nil {
			progress.err = err
			progress.cancel()
		}
		progress.mutex.Unlock()
	}
}

func (progress *progressCapture) Err() error {
	progress.mutex.Lock()
	defer progress.mutex.Unlock()
	return progress.err
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func toolNameSet(values []agent.ToolName) map[agent.ToolName]struct{} {
	result := make(map[agent.ToolName]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func decodeMCPArguments(encoded agent.JSONObject) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(encoded.String()))
	decoder.UseNumber()
	var arguments map[string]any
	if err := decoder.Decode(&arguments); err != nil {
		return nil, fmt.Errorf("decode MCP tool arguments: %w", err)
	}
	if arguments == nil {
		return nil, errors.New("MCP tool arguments must be an object")
	}
	return arguments, nil
}

func waitForRetry(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func errorTypeName(err error) string {
	name := fmt.Sprintf("%T", err)
	name = strings.TrimPrefix(name, "*")
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[index+1:]
	}
	if name == "" {
		return "error"
	}
	return name
}

type mcpSDKLogHandler struct {
	next slog.Handler
}

func (handler mcpSDKLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler mcpSDKLogHandler) Handle(ctx context.Context, record slog.Record) error {
	sanitized := slog.NewRecord(record.Time, record.Level, "MCP SDK: "+record.Message, record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		if attribute.Key == "error" {
			if err, ok := attribute.Value.Any().(error); ok {
				sanitized.AddAttrs(slog.String("error_type", errorTypeName(err)))
			}
		}
		return true
	})
	return handler.next.Handle(ctx, sanitized)
}

func (handler mcpSDKLogHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return handler
}

func (handler mcpSDKLogHandler) WithGroup(_ string) slog.Handler {
	return handler
}

var _ agent.ToolRegistry = (*Registry)(nil)
