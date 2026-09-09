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
