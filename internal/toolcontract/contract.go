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

// Package toolcontract owns generated built-in tool input contracts.
package toolcontract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/superdurable/superagent/internal/toolcontract/generated"
)

// Contract binds one provider-visible schema to its generated Go input type.
type Contract[T any] struct {
	inputSchema string
	validate    func(*T) error
}

// InputSchema returns the compact provider-visible JSON Schema object.
func (contract Contract[T]) InputSchema() string {
	return contract.inputSchema
}

// Decode strictly decodes and validates one tool input object.
func (contract Contract[T]) Decode(encoded string) (T, error) {
	var input T
	if err := json.Unmarshal([]byte(encoded), &input); err != nil {
		return input, err
	}
	if err := contract.validate(&input); err != nil {
		return input, err
	}
	return input, nil
}

//go:embed schema/write_todos.json
var writeTodosSchema []byte

//go:embed schema/durable_wait.json
var durableWaitSchema []byte

//go:embed schema/request_user_input.json
var requestUserInputSchema []byte

// WriteTodos is the generated write_todos input contract.
var WriteTodos = mustContract(writeTodosSchema, (*generated.WriteTodosInput).Validate)

// DurableWait is the generated durable_wait input contract.
var DurableWait = mustContract(durableWaitSchema, (*generated.DurableWaitInput).Validate)

// RequestUserInput is the generated request_user_input input contract.
var RequestUserInput = mustContract(requestUserInputSchema, (*generated.RequestUserInputInput).Validate)

func mustContract[T any](encoded []byte, validate func(*T) error) Contract[T] {
	compact := bytes.NewBuffer(make([]byte, 0, len(encoded)))
	if err := json.Compact(compact, encoded); err != nil {
		panic(fmt.Errorf("compact tool input schema: %w", err))
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(compact.Bytes(), &object); err != nil || object == nil {
		panic("tool input schema must be one complete JSON object")
	}
	return Contract[T]{
		inputSchema: compact.String(),
		validate:    validate,
	}
}
