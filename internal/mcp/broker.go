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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/superdurable/superagent/internal/agent"
)

const (
	brokerListResources         agent.ToolName = "mcp_list_resources"
	brokerReadResource          agent.ToolName = "mcp_read_resource"
	brokerListResourceTemplates agent.ToolName = "mcp_list_resource_templates"
	brokerListPrompts           agent.ToolName = "mcp_list_prompts"
	brokerGetPrompt             agent.ToolName = "mcp_get_prompt"
)

var brokerNames = map[agent.ToolName]struct{}{
	brokerListResources:         {},
	brokerReadResource:          {},
	brokerListResourceTemplates: {},
	brokerListPrompts:           {},
	brokerGetPrompt:             {},
}

type brokerArguments struct {
	Server    string            `json:"server"`
	URI       string            `json:"uri,omitempty"`
	Name      string            `json:"name,omitempty"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

func (registry *Registry) executeBroker(
	ctx context.Context,
	invocation agent.ToolInvocation,
	enabledServers map[string]struct{},
	servers map[string]ServerConfig,
) (agent.ToolExecutionResult, error) {
	arguments, err := decodeBrokerArguments(invocation)
	if err != nil {
		return agent.ToolExecutionResult{}, err
	}
	server, configured := servers[arguments.Server]
	_, enabled := enabledServers[arguments.Server]
	if !configured || !enabled {
		return agent.ToolExecutionResult{}, fmt.Errorf("MCP server %q is not enabled", arguments.Server)
	}
	operationCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	session, err := registry.connect(operationCtx, server, nil)
	if err != nil {
		return agent.ToolExecutionResult{}, err
	}
	encoded, operationErr := invokeBroker(operationCtx, session, invocation.Name, arguments)
	closeErr := session.Close()
	if operationErr != nil {
		return agent.ToolExecutionResult{}, errors.Join(operationErr, closeErr)
	}
	if closeErr != nil {
		registry.logger.WarnContext(ctx, "close MCP broker session after result", "server", server.Name, "error", closeErr)
	}
	return agent.ToolExecutionResult{
		Content: string(encoded),
		Outcome: agent.ToolOutcomeSucceeded,
	}, nil
}

func invokeBroker(
	ctx context.Context,
	session *mcpsdk.ClientSession,
	name agent.ToolName,
	arguments brokerArguments,
) ([]byte, error) {
	switch name {
	case brokerListResources:
		result, err := session.ListResources(ctx, nil)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		return encoded, wrapBrokerEncodingError(err)
	case brokerReadResource:
		if arguments.URI == "" {
			return nil, errors.New("mcp_read_resource requires uri")
		}
		result, err := session.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: arguments.URI})
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		return encoded, wrapBrokerEncodingError(err)
	case brokerListResourceTemplates:
		result, err := session.ListResourceTemplates(ctx, nil)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		return encoded, wrapBrokerEncodingError(err)
	case brokerListPrompts:
		result, err := session.ListPrompts(ctx, nil)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		return encoded, wrapBrokerEncodingError(err)
	case brokerGetPrompt:
		if arguments.Name == "" {
			return nil, errors.New("mcp_get_prompt requires name")
		}
		result, err := session.GetPrompt(ctx, &mcpsdk.GetPromptParams{
			Name:      arguments.Name,
			Arguments: arguments.Arguments,
		})
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		return encoded, wrapBrokerEncodingError(err)
	default:
		return nil, fmt.Errorf("unknown MCP broker tool %q", name)
	}
}

func wrapBrokerEncodingError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("encode MCP broker result: %w", err)
}

func decodeBrokerArguments(invocation agent.ToolInvocation) (brokerArguments, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(invocation.Arguments.String()))
	decoder.DisallowUnknownFields()
	var arguments brokerArguments
	if err := decoder.Decode(&arguments); err != nil {
		return brokerArguments{}, fmt.Errorf("decode %q arguments: %w", invocation.Name, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return brokerArguments{}, fmt.Errorf("%q arguments contain trailing JSON", invocation.Name)
		}
		return brokerArguments{}, fmt.Errorf("decode %q trailing arguments: %w", invocation.Name, err)
	}
	arguments.Server = strings.TrimSpace(arguments.Server)
	arguments.URI = strings.TrimSpace(arguments.URI)
	arguments.Name = strings.TrimSpace(arguments.Name)
	if arguments.Server == "" {
		return brokerArguments{}, fmt.Errorf("%q requires server", invocation.Name)
	}
	return arguments, nil
}

func brokerDefinitions() []agent.ToolDefinition {
	const listSchema = `{
		"type":"object",
		"properties":{"server":{"type":"string","minLength":1}},
		"required":["server"],
		"additionalProperties":false
	}`
	return []agent.ToolDefinition{
		brokerDefinition(brokerListResources, "List resources exposed by an enabled MCP server.", listSchema),
		brokerDefinition(brokerReadResource, "Read one resource from an enabled MCP server.", `{
			"type":"object",
			"properties":{"server":{"type":"string","minLength":1},"uri":{"type":"string","minLength":1}},
			"required":["server","uri"],
			"additionalProperties":false
		}`),
		brokerDefinition(brokerListResourceTemplates, "List resource templates exposed by an enabled MCP server.", listSchema),
		brokerDefinition(brokerListPrompts, "List prompts exposed by an enabled MCP server.", listSchema),
		brokerDefinition(brokerGetPrompt, "Render a prompt from an enabled MCP server as tool data.", `{
			"type":"object",
			"properties":{
				"server":{"type":"string","minLength":1},
				"name":{"type":"string","minLength":1},
				"arguments":{"type":"object","additionalProperties":{"type":"string"}}
			},
			"required":["server","name"],
			"additionalProperties":false
		}`),
	}
}

func brokerDefinition(name agent.ToolName, description string, schema string) agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:               name,
		Description:        description,
		InputSchema:        agent.MustJSONObject(schema),
		AttemptTimeout:     time.Minute,
		MaximumAttempts:    3,
		RetryTotalDuration: 5 * time.Minute,
	}
}
