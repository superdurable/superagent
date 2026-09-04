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

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/superdurable/superagent/internal/app"
	"github.com/superdurable/superagent/internal/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	applicationConfig, err := config.Load()
	if err != nil {
		logger.ErrorContext(ctx, "configuration rejected", slog.String("error", err.Error()))
		return 1
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, applicationConfig, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.ErrorContext(ctx, "SuperAgent stopped", slog.String("error", err.Error()))
		return 1
	}
	return 0
}
