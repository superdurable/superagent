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
	"testing"

	"github.com/superdurable/dex/sdk-go/dex"
)

func TestAgentStartFlowOptionsDefaultStepsToAsyncDurability(t *testing.T) {
	t.Parallel()
	options := newAgentStartFlowOptions(nil)
	if options.ConfigOverride == nil || options.ConfigOverride.StepDurability == nil {
		t.Fatalf("ConfigOverride = %+v", options.ConfigOverride)
	}
	if got := *options.ConfigOverride.StepDurability; got != dex.StepDurabilityAsync {
		t.Fatalf("StepDurability = %v, want %v", got, dex.StepDurabilityAsync)
	}
}

func TestListRecentEventsRejectsInvalidLimits(t *testing.T) {
	t.Parallel()
	client := &Client{}
	for _, limit := range []int{0, MaximumRecentEventLimit + 1} {
		_, err := client.ListRecentEvents(t.Context(), "flow-1", EventStreamActivity, limit)
		if err == nil {
			t.Fatalf("limit %d error = nil", limit)
		}
	}
}

func TestGetArchivedMessageRangeRejectsInvalidLimits(t *testing.T) {
	t.Parallel()
	client := &Client{}
	for _, limit := range []int{0, 9, 11, 210} {
		_, err := client.GetArchivedMessageRange(t.Context(), "flow-1", 11, limit)
		if err == nil {
			t.Fatalf("limit %d error = nil", limit)
		}
	}
}
