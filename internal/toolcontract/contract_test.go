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

package toolcontract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/superdurable/superagent/internal/toolcontract/generated"
)

func TestWriteTodosContract(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		wantErr bool
	}{
		{name: "empty array", encoded: `{"todos":[]}`},
		{name: "valid task", encoded: `{"todos":[{"content":"ship","status":"in_progress"}]}`},
		{name: "missing todos", encoded: `{}`, wantErr: true},
		{name: "null todos", encoded: `{"todos":null}`, wantErr: true},
		{name: "unknown field", encoded: `{"todos":[],"extra":true}`, wantErr: true},
		{name: "wrong todos type", encoded: `{"todos":{}}`, wantErr: true},
		{name: "missing content", encoded: `{"todos":[{"status":"pending"}]}`, wantErr: true},
		{name: "empty content", encoded: `{"todos":[{"content":"","status":"pending"}]}`, wantErr: true},
		{name: "invalid status", encoded: `{"todos":[{"content":"ship","status":"active"}]}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, err := WriteTodos.Decode(test.encoded)
			if (err != nil) != test.wantErr {
				t.Fatalf("Decode() error = %v, wantErr %t", err, test.wantErr)
			}
			if !test.wantErr && test.name == "valid task" &&
				(len(input.Todos) != 1 || input.Todos[0].Status != generated.WriteTodosInputTodosItemStatusInProgress) {
				t.Fatalf("Decode() = %#v", input)
			}
		})
	}
}

func TestDurableWaitContract(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		wantErr bool
	}{
		{name: "minimum", encoded: `{"duration_seconds":1,"reason":"retry"}`},
		{name: "missing duration", encoded: `{"reason":"retry"}`, wantErr: true},
		{name: "null duration", encoded: `{"duration_seconds":null,"reason":"retry"}`, wantErr: true},
		{name: "zero duration", encoded: `{"duration_seconds":0,"reason":"retry"}`, wantErr: true},
		{name: "fractional duration", encoded: `{"duration_seconds":1.5,"reason":"retry"}`, wantErr: true},
		{name: "missing reason", encoded: `{"duration_seconds":1}`, wantErr: true},
		{name: "null reason", encoded: `{"duration_seconds":1,"reason":null}`, wantErr: true},
		{name: "unknown field", encoded: `{"duration_seconds":1,"reason":"retry","extra":true}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DurableWait.Decode(test.encoded)
			if (err != nil) != test.wantErr {
				t.Fatalf("Decode() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestRequestUserInputContract(t *testing.T) {
	validQuestion := `{"id":"region","header":"Region","question":"Where?","options":[{"label":"West","description":"Use west."},{"label":"East","description":"Use east."}]}`
	tests := []struct {
		name    string
		encoded string
		wantErr bool
	}{
		{name: "minimums", encoded: `{"questions":[` + validQuestion + `]}`},
		{name: "maximum header length", encoded: `{"questions":[{"id":"region","header":"123456789012","question":"Where?","options":[{"label":"West","description":"Use west."},{"label":"East","description":"Use east."}]}]}`},
		{name: "missing questions", encoded: `{}`, wantErr: true},
		{name: "null questions", encoded: `{"questions":null}`, wantErr: true},
		{name: "no questions", encoded: `{"questions":[]}`, wantErr: true},
		{name: "too many questions", encoded: `{"questions":[` + validQuestion + `,` + validQuestion + `,` + validQuestion + `,` + validQuestion + `]}`, wantErr: true},
		{name: "empty ID", encoded: `{"questions":[{"id":"","header":"Region","question":"Where?","options":[{"label":"West","description":"Use west."},{"label":"East","description":"Use east."}]}]}`, wantErr: true},
		{name: "empty header", encoded: `{"questions":[{"id":"region","header":"","question":"Where?","options":[{"label":"West","description":"Use west."},{"label":"East","description":"Use east."}]}]}`, wantErr: true},
		{name: "header too long", encoded: `{"questions":[{"id":"region","header":"1234567890123","question":"Where?","options":[{"label":"West","description":"Use west."},{"label":"East","description":"Use east."}]}]}`, wantErr: true},
		{name: "empty question", encoded: `{"questions":[{"id":"region","header":"Region","question":"","options":[{"label":"West","description":"Use west."},{"label":"East","description":"Use east."}]}]}`, wantErr: true},
		{name: "one option", encoded: `{"questions":[{"id":"region","header":"Region","question":"Where?","options":[{"label":"West","description":"Use west."}]}]}`, wantErr: true},
		{name: "four options", encoded: `{"questions":[{"id":"region","header":"Region","question":"Where?","options":[{"label":"1","description":"1"},{"label":"2","description":"2"},{"label":"3","description":"3"},{"label":"4","description":"4"}]}]}`, wantErr: true},
		{name: "empty option label", encoded: `{"questions":[{"id":"region","header":"Region","question":"Where?","options":[{"label":"","description":"Use west."},{"label":"East","description":"Use east."}]}]}`, wantErr: true},
		{name: "empty option description", encoded: `{"questions":[{"id":"region","header":"Region","question":"Where?","options":[{"label":"West","description":""},{"label":"East","description":"Use east."}]}]}`, wantErr: true},
		{name: "unknown nested field", encoded: `{"questions":[{"id":"region","header":"Region","question":"Where?","options":[{"label":"West","description":"Use west.","extra":true},{"label":"East","description":"Use east."}]}]}`, wantErr: true},
		{name: "wrong type", encoded: `{"questions":"ask"}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RequestUserInput.Decode(test.encoded)
			if (err != nil) != test.wantErr {
				t.Fatalf("Decode() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestProviderSchemasAreMinimalObjects(t *testing.T) {
	for name, schema := range map[string]string{
		"write_todos":        WriteTodos.InputSchema(),
		"durable_wait":       DurableWait.InputSchema(),
		"request_user_input": RequestUserInput.InputSchema(),
	} {
		t.Run(name, func(t *testing.T) {
			var object map[string]json.RawMessage
			if err := json.Unmarshal([]byte(schema), &object); err != nil || object == nil {
				t.Fatalf("schema is not an object: %v", err)
			}
			for _, forbidden := range []string{`"$schema"`, `"$ref"`, `"$defs"`, `"x-ogen-`} {
				if strings.Contains(schema, forbidden) {
					t.Fatalf("schema contains %s: %s", forbidden, schema)
				}
			}
			if strings.Contains(schema, "\n") {
				t.Fatalf("schema is not compact: %q", schema)
			}
		})
	}
}
