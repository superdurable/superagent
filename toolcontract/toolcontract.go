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

// Package toolcontract exposes the generated contracts for SuperAgent's built-in tools.
package toolcontract

import (
	contractinternal "github.com/superdurable/superagent/internal/toolcontract"
	"github.com/superdurable/superagent/internal/toolcontract/generated"
)

// Public types alias the generated contract DTOs without duplicating their JSON behavior.
type (
	WriteTodosInput          = generated.WriteTodosInput
	WriteTodosItem           = generated.WriteTodosInputTodosItem
	WriteTodosStatus         = generated.WriteTodosInputTodosItemStatus
	DurableWaitInput         = generated.DurableWaitInput
	RequestUserInput         = generated.RequestUserInputInput
	RequestUserInputQuestion = generated.RequestUserInputInputQuestionsItem
	RequestUserInputOption   = generated.RequestUserInputInputQuestionsItemOptionsItem
)

const (
	// WriteTodosStatusPending marks work that has not started.
	WriteTodosStatusPending = generated.WriteTodosInputTodosItemStatusPending
	// WriteTodosStatusInProgress marks work currently being performed.
	WriteTodosStatusInProgress = generated.WriteTodosInputTodosItemStatusInProgress
	// WriteTodosStatusCompleted marks finished work.
	WriteTodosStatusCompleted = generated.WriteTodosInputTodosItemStatusCompleted
)

// WriteTodosInputSchema returns the compact provider-visible write_todos schema.
func WriteTodosInputSchema() string {
	return contractinternal.WriteTodos.InputSchema()
}

// DecodeWriteTodosInput strictly decodes and validates write_todos arguments.
func DecodeWriteTodosInput(encoded string) (WriteTodosInput, error) {
	return contractinternal.WriteTodos.Decode(encoded)
}

// DurableWaitInputSchema returns the compact provider-visible durable_wait schema.
func DurableWaitInputSchema() string {
	return contractinternal.DurableWait.InputSchema()
}

// DecodeDurableWaitInput strictly decodes and validates durable_wait arguments.
func DecodeDurableWaitInput(encoded string) (DurableWaitInput, error) {
	return contractinternal.DurableWait.Decode(encoded)
}

// RequestUserInputSchema returns the compact provider-visible request_user_input schema.
func RequestUserInputSchema() string {
	return contractinternal.RequestUserInput.InputSchema()
}

// DecodeRequestUserInput strictly decodes and validates request_user_input arguments.
func DecodeRequestUserInput(encoded string) (RequestUserInput, error) {
	return contractinternal.RequestUserInput.Decode(encoded)
}
