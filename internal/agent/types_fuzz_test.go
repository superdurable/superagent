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

package agent

import (
	"encoding/json"
	"testing"
)

func FuzzDomainDecoders(fuzzer *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"duration_seconds":1,"reason":"wait"}`,
		`{"prompt":"choose","choices":["one","two"]}`,
		`{"todos":[{"content":"ship","status":"pending"}]}`,
		`"waiting_for_message"`,
		`null`,
		``,
	} {
		fuzzer.Add(seed)
	}
	fuzzer.Fuzz(func(t *testing.T, encoded string) {
		object, err := ParseJSONObject(encoded)
		if err == nil {
			var decoded map[string]json.RawMessage
			if unmarshalErr := json.Unmarshal([]byte(object), &decoded); unmarshalErr != nil {
				t.Fatalf("accepted JSONObject cannot be decoded: %v", unmarshalErr)
			}
			call := ToolCall{ID: "fuzz-call", Name: ToolNameWriteTodos, Arguments: object}
			_, _ = planTasks(call)
			call.Name = ToolNameDurableWait
			_, _ = durableWaitArgumentsFor(call)
			call.Name = ToolNameRequestUserInput
			_, _ = userInputArgumentsFor(call)
		}

		var status AgentStatus
		if unmarshalErr := json.Unmarshal([]byte(encoded), &status); unmarshalErr == nil {
			if validateErr := status.Validate(); validateErr != nil {
				t.Fatalf("accepted AgentStatus is invalid: %v", validateErr)
			}
		}
	})
}
