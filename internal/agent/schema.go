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

import "github.com/superdurable/dex/sdk-go/dex"

var (
	agentConfigAttribute       = dex.DefineAttribute[AgentConfig]("AgentConfig")
	agentStateAttribute        = dex.DefineAttribute[AgentState]("AgentState")
	contextSummaryAttribute    = dex.DefineAttribute[ContextSummary]("ContextSummary")
	agentMessagesAttribute     = dex.DefineAttributeMap[AgentMessage]("AgentMessages")
	agentPlanAttribute         = dex.DefineAttribute[AgentPlan]("AgentPlan")
	pendingApprovalAttribute   = dex.DefineAttribute[PendingApproval]("PendingApproval")
	pendingTimerAttribute      = dex.DefineAttribute[PendingTimer]("PendingTimer")
	pendingUserInputAttribute  = dex.DefineAttribute[PendingUserInput]("PendingUserInput")
	queuedUserMessagesChannel  = dex.DefineChannel[UserMessage]("QueuedUserMessages")
	steeredUserMessagesChannel = dex.DefineChannel[UserMessage]("SteeredUserMessages")
	toolApprovalsChannel       = dex.DefineChannelMap[ToolApproval]("ToolApprovals")
	planExecutionsChannel      = dex.DefineChannelMap[PlanExecutionRequest]("PlanExecutions")
	reasoningSummaryStream     = dex.DefineStream[string]("ReasoningSummary", 10<<20)
	assistantTextStream        = dex.DefineStream[string]("AssistantText", 10<<20)
	agentActivityStream        = dex.DefineStream[AgentEvent]("AgentActivity", 10<<20)
)
