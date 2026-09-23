/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

export {
  ActivityRow,
  activityIcon,
  activityLabel,
  formatActivityTime,
  type ActivityRowProps,
} from "./ActivityRow.js";
export {
  ApprovalCard,
  TimerCard,
  type ApprovalCardProps,
  type TimerCardProps,
} from "./ApprovalTimerCards.js";
export {
  ConversationComposer,
  type ConversationComposerProps,
  type ConversationSubmitShortcut,
} from "./ConversationComposer.js";
export {
  ConversationTimeline,
  type ConversationTimelineProps,
} from "./ConversationTimelineView.js";
export {
  ConversationView,
  type ConversationViewProps,
} from "./ConversationView.js";
export { MarkdownContent } from "./MarkdownContent.js";
export {
  PlanPanel,
  type PlanPanelProps,
  type PlanPanelTask,
} from "./PlanPanel.js";
export {
  planActionPresentation,
  type PlanActionGates,
  type PlanActionPresentation,
  type PlanStatusValue,
  type PlanTaskStatusValue,
} from "./planAction.js";
export {
  PendingQuestionBatch,
  type PendingQuestion,
  type PendingQuestionAnswer,
  type PendingQuestionBatchProps,
  type PendingQuestionOption,
} from "./PendingQuestionBatch.js";
export {
  PendingMessageQueue,
  type PendingMessageAction,
  type PendingMessageQueueItem,
  type PendingMessageQueueProps,
} from "./PendingMessageQueue.js";
export {
  ToolRecoveryPanel,
  type PendingToolRecovery,
  type ToolRecoveryAction,
  type ToolRecoveryCall,
  type ToolRecoveryDecision,
  type ToolRecoveryPanelProps,
  type ToolRecoveryResolution,
} from "./ToolRecoveryPanel.js";
export {
  ToolCallCard,
  ApplyPatchDiff,
  ExecCommandCard,
  type ToolCallCardProps,
} from "./ToolCallCard.js";
export {
  buildConversationTimeline,
  type ConversationTimelineEntry,
  type TimelineActivityEntry,
  type TimelineActivityEvent,
  type TimelineConsumedUserEntry,
  type TimelineLiveTextEntry,
  type TimelineMessage,
  type TimelineMessageRole,
  type TimelineSequencedMessage,
  type TimelineToolCall,
} from "./conversationTimeline.js";
export {
  buildConversationPresentation,
  formatDuration,
  toolCallFingerprint,
  unionDuration,
  type ConversationModelWorkItem,
  type ConversationOperationStatus,
  type ConversationPendingWait,
  type ConversationPresentation,
  type ConversationPresentationInput,
  type ConversationQuestion,
  type ConversationQuestionItem,
  type ConversationQuestionOption,
  type ConversationStreamState,
  type ConversationToolGroup,
  type ConversationToolWorkItem,
  type ConversationTurn,
  type ConversationWorkItem,
} from "./conversationPresentation.js";
export {
  indexToolResultsByCallId,
  isPairedToolResultMessage,
  pairToolCallsById,
  type ToolCallPair,
  type ToolCallResultView,
} from "./pairToolCalls.js";
export {
  parsePatchDiff,
  summarizePatchFiles,
  type DiffLine,
  type DiffLineKind,
  type PatchFileDiff,
} from "./parsePatchDiff.js";
export {
  formatShellCommand,
  parseJsonObject,
  parseToolArguments,
  projectCommandOutput,
  stripAnsi,
} from "./toolPayload.js";
export {
  appendLiveText,
  completeLiveText,
  mergeActivityEvent,
  mergeSequencedMessages,
  type ActivityEventLike,
  type LiveTextLike,
  type SequencedMessageLike,
} from "./viewStateMerge.js";
export {
  useTimelineFollow,
  type ScrollRoot,
  type TimelineFollowOptions,
  type TimelineFollowState,
} from "./useTimelineFollow.js";
