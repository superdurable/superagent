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
	"strings"
	"testing"
)

func TestCondenseActivityMessageProducesBoundedSingleLineSummary(t *testing.T) {
	raw := "tool arguments:\n" + strings.Repeat("private payload ", 30)
	summary := condenseActivityMessage(raw)
	if strings.Contains(summary, "\n") {
		t.Fatalf("summary contains newline: %q", summary)
	}
	if len([]rune(summary)) > 200 || !strings.HasSuffix(summary, "…") {
		t.Fatalf("summary is not bounded: %d runes, %q", len([]rune(summary)), summary)
	}
}

func TestPlanTaskActivitiesIdentifyOnlyStableChangedTasks(t *testing.T) {
	previous := &AgentPlan{
		Revision: 7,
		Status:   PlanStatusActive,
		Tasks: []PlanTask{
			{Content: "inspect", Status: TaskStatusPending},
			{Content: "implement", Status: TaskStatusInProgress},
			{Content: "old verification", Status: TaskStatusPending},
		},
	}
	events := planTaskActivities(previous, 8, []PlanTask{
		{Content: "inspect", Status: TaskStatusInProgress},
		{Content: "implement", Status: TaskStatusCompleted},
		{Content: "new verification", Status: TaskStatusInProgress},
		{Content: "publish", Status: TaskStatusPending},
	})
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	for index, event := range events {
		if event.Kind != EventKindPlanTaskUpdated || event.PlanBaseRevision == nil ||
			*event.PlanBaseRevision != 7 || event.PlanRevision == nil || *event.PlanRevision != 8 ||
			event.PlanTaskIndex == nil || int(*event.PlanTaskIndex) != index || event.PlanTaskStatus == nil {
			t.Fatalf("event %d = %#v", index, event)
		}
	}
	if *events[0].PlanTaskStatus != TaskStatusInProgress ||
		*events[1].PlanTaskStatus != TaskStatusCompleted {
		t.Fatalf("statuses = %#v", events)
	}
	if events[0].Message != "Started plan task 1." || events[1].Message != "Completed plan task 2." {
		t.Fatalf("messages = %#v", events)
	}
}

func TestPlanTaskActivitiesRequireAPreviousPlan(t *testing.T) {
	if events := planTaskActivities(nil, 1, []PlanTask{{
		Content: "inspect",
		Status:  TaskStatusInProgress,
	}}); len(events) != 0 {
		t.Fatalf("events = %#v", events)
	}
}
