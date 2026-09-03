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
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"

	"golang.org/x/net/http/httpguts"
	"gopkg.in/yaml.v3"
)

const (
	maximumToolAttempts     = 10
	maximumToolTimeout      = 24 * 60 * 60
	maximumToolRetrySeconds = 7 * 24 * 60 * 60
)

var environmentVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Transport identifies a supported MCP transport.
type Transport string

const (
	// TransportStdio runs one local subprocess per MCP session.
	TransportStdio Transport = "stdio"
	// TransportStreamableHTTP uses MCP Streamable HTTP without SDK retries.
	TransportStreamableHTTP Transport = "streamable_http"
)

// Validate rejects unknown MCP transports.
func (transport Transport) Validate() error {
	switch transport {
	case TransportStdio, TransportStreamableHTTP:
		return nil
	default:
		return &TransportValidationError{Value: string(transport)}
	}
}

// UnmarshalYAML decodes and validates an MCP transport.
func (transport *Transport) UnmarshalYAML(node *yaml.Node) error {
	var value string
	if err := node.Decode(&value); err != nil {
		return err
	}
	decoded := Transport(value)
	if err := decoded.Validate(); err != nil {
		return err
	}
	*transport = decoded
	return nil
}

// TransportValidationError identifies one unsupported transport value.
type TransportValidationError struct {
	Value string
}

// Error describes the unsupported transport.
func (err *TransportValidationError) Error() string {
	return fmt.Sprintf("unsupported MCP transport %q", err.Value)
}

// ToolPolicy configures safety and bounded retries for one tool.
type ToolPolicy struct {
	// ReadOnly overrides the tool annotation; nil means unknown.
	ReadOnly *bool `yaml:"read_only"`
	// TimeoutSeconds defaults to 60 and bounds one attempt.
	TimeoutSeconds float64 `yaml:"timeout_seconds"`
	// MaximumAttempts defaults to three for trusted reads and one otherwise.
	MaximumAttempts *int `yaml:"maximum_attempts"`
	// RetryTotalSeconds defaults to 300 and bounds all attempts.
	RetryTotalSeconds float64 `yaml:"retry_total_seconds"`
}

// ServerConfig is one trusted Worker-side MCP connection.
type ServerConfig struct {
	// Name is the unique stable server identity.
	Name string `yaml:"name"`
	// Transport selects stdio or Streamable HTTP.
	Transport Transport `yaml:"transport"`
	// Command starts a stdio server and is forbidden for HTTP.
	Command string `yaml:"command,omitempty"`
	// Args are passed directly to Command without shell expansion.
	Args []string `yaml:"args,omitempty"`
	// CWD optionally sets the stdio subprocess working directory.
	CWD string `yaml:"cwd,omitempty"`
	// Environment maps subprocess variable names to source variable names.
	Environment map[string]string `yaml:"env,omitempty"`
	// URL is the absolute HTTP endpoint and is forbidden for stdio.
	URL string `yaml:"url,omitempty"`
	// Headers maps HTTP header names to source environment variable names.
	Headers map[string]string `yaml:"headers,omitempty"`
	// TrustReadOnlyAnnotations allows server annotations to disable approval.
	TrustReadOnlyAnnotations bool `yaml:"trust_read_only_annotations,omitempty"`
	// Tools contains per-remote-name policy overrides.
	Tools map[string]ToolPolicy `yaml:"tools,omitempty"`
}

type fileConfig struct {
	Servers []ServerConfig `yaml:"servers"`
}

// LoadConfig decodes trusted MCP configuration from path.
func LoadConfig(path string) ([]ServerConfig, error) {
	if path == "" {
		return []ServerConfig{}, nil
	}
	// The path comes only from trusted process configuration.
	contents, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read MCP config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var config fileConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode MCP config: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("MCP config must contain one YAML document")
		}
		return nil, fmt.Errorf("decode trailing MCP config: %w", err)
	}
	seen := make(map[string]struct{}, len(config.Servers))
	for index := range config.Servers {
		server := &config.Servers[index]
		applyDefaults(server)
		if err := validateServer(*server); err != nil {
			return nil, fmt.Errorf("MCP server %d: %w", index, err)
		}
		if _, found := seen[server.Name]; found {
			return nil, fmt.Errorf("MCP server name %q is duplicated", server.Name)
		}
		seen[server.Name] = struct{}{}
	}
	return config.Servers, nil
}

func applyDefaults(server *ServerConfig) {
	if server.Args == nil {
		server.Args = []string{}
	}
	if server.Environment == nil {
		server.Environment = map[string]string{}
	}
	if server.Headers == nil {
		server.Headers = map[string]string{}
	}
	if server.Tools == nil {
		server.Tools = map[string]ToolPolicy{}
	}
	for name, policy := range server.Tools {
		if policy.TimeoutSeconds == 0 {
			policy.TimeoutSeconds = 60
		}
		if policy.RetryTotalSeconds == 0 {
			policy.RetryTotalSeconds = 300
		}
		server.Tools[name] = policy
	}
}

func cloneServerConfig(server ServerConfig) ServerConfig {
	server.Args = append([]string(nil), server.Args...)
	server.Environment = cloneStringMap(server.Environment)
	server.Headers = cloneStringMap(server.Headers)
	server.Tools = cloneToolPolicies(server.Tools)
	return server
}

func validateServer(server ServerConfig) error {
	if strings.TrimSpace(server.Name) == "" {
		return errors.New("MCP server name must not be empty")
	}
	if err := server.Transport.Validate(); err != nil {
		return err
	}
	switch server.Transport {
	case TransportStdio:
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("stdio MCP server %q requires command", server.Name)
		}
		if server.URL != "" || len(server.Headers) != 0 {
			return fmt.Errorf("stdio MCP server %q cannot set HTTP fields", server.Name)
		}
	case TransportStreamableHTTP:
		if err := validateHTTPServer(server); err != nil {
			return err
		}
		if server.Command != "" || len(server.Args) != 0 || server.CWD != "" || len(server.Environment) != 0 {
			return fmt.Errorf("HTTP MCP server %q cannot set stdio fields", server.Name)
		}
	}
	if err := validateEnvironmentMapping("env", server.Environment); err != nil {
		return err
	}
	if err := validateEnvironmentMapping("headers", server.Headers); err != nil {
		return err
	}
	for name, policy := range server.Tools {
		if strings.TrimSpace(name) == "" {
			return errors.New("MCP tool policy name must not be empty")
		}
		if !isFinitePositive(policy.TimeoutSeconds) || policy.TimeoutSeconds > maximumToolTimeout {
			return fmt.Errorf("timeout_seconds for %q must be positive and at most %d", name, maximumToolTimeout)
		}
		if !isFinitePositive(policy.RetryTotalSeconds) || policy.RetryTotalSeconds > maximumToolRetrySeconds {
			return fmt.Errorf("retry_total_seconds for %q must be positive and at most %d", name, maximumToolRetrySeconds)
		}
		if policy.MaximumAttempts != nil && (*policy.MaximumAttempts <= 0 || *policy.MaximumAttempts > maximumToolAttempts) {
			return fmt.Errorf("maximum_attempts for %q must be between 1 and %d", name, maximumToolAttempts)
		}
	}
	return nil
}

func validateHTTPServer(server ServerConfig) error {
	parsed, err := url.Parse(server.URL)
	if err != nil {
		return fmt.Errorf("HTTP MCP server %q has invalid url: %w", server.Name, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("HTTP MCP server %q requires an absolute http or https url", server.Name)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("HTTP MCP server %q url cannot contain user information or a fragment", server.Name)
	}
	for name := range server.Headers {
		if !httpguts.ValidHeaderFieldName(name) || strings.EqualFold(name, "Host") {
			return fmt.Errorf("HTTP MCP server %q has an invalid header name", server.Name)
		}
	}
	return nil
}

func validateEnvironmentMapping(kind string, mapping map[string]string) error {
	for targetName, sourceName := range mapping {
		if kind == "env" && !environmentVariableName.MatchString(targetName) {
			return fmt.Errorf("MCP env target %q is not a valid environment variable name", targetName)
		}
		if !environmentVariableName.MatchString(sourceName) {
			return fmt.Errorf("MCP %s source %q is not a valid environment variable name", kind, sourceName)
		}
	}
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneToolPolicies(source map[string]ToolPolicy) map[string]ToolPolicy {
	cloned := make(map[string]ToolPolicy, len(source))
	for name, policy := range source {
		if policy.MaximumAttempts != nil {
			attempts := *policy.MaximumAttempts
			policy.MaximumAttempts = &attempts
		}
		if policy.ReadOnly != nil {
			readOnly := *policy.ReadOnly
			policy.ReadOnly = &readOnly
		}
		cloned[name] = policy
	}
	return cloned
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func resolveEnvironment(mapping map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(mapping))
	for targetName, sourceName := range mapping {
		value, found := os.LookupEnv(sourceName)
		if !found {
			return nil, fmt.Errorf("required environment variable %q is unset", sourceName)
		}
		result[targetName] = value
	}
	return result, nil
}
