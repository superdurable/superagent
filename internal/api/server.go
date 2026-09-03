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

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	transportapi "github.com/superdurable/superagent/internal/api/generated"
)

const maximumRequestBodyBytes = 2 << 20

// NewHTTPHandler constructs a generated-contract server with bounded bodies and JSON errors.
func NewHTTPHandler(handler *Handler, logger *slog.Logger) (http.Handler, error) {
	if handler == nil || logger == nil {
		panic("API server dependencies are required")
	}
	errorHandler := func(ctx context.Context, writer http.ResponseWriter, _ *http.Request, err error) {
		logger.WarnContext(ctx, "request rejected by OpenAPI validation", slog.String("error_type", fmt.Sprintf("%T", err)))
		if writeErr := writeProblem(writer, http.StatusBadRequest, "Bad Request", "request does not match the OpenAPI contract"); writeErr != nil {
			logger.ErrorContext(ctx, "write OpenAPI validation response", slog.Any("error", writeErr))
		}
	}
	notFound := func(writer http.ResponseWriter, request *http.Request) {
		if err := writeProblem(writer, http.StatusNotFound, "Not Found", "the requested resource does not exist"); err != nil {
			logger.ErrorContext(request.Context(), "write not-found response", slog.Any("error", err))
		}
	}
	methodNotAllowed := func(writer http.ResponseWriter, request *http.Request, allowed string) {
		writer.Header().Set("Allow", allowed)
		if err := writeProblem(writer, http.StatusMethodNotAllowed, "Method Not Allowed", "the request method is not allowed"); err != nil {
			logger.ErrorContext(request.Context(), "write method-not-allowed response", slog.Any("error", err))
		}
	}
	server, err := transportapi.NewServer(
		handler,
		transportapi.WithErrorHandler(errorHandler),
		transportapi.WithNotFound(notFound),
		transportapi.WithMethodNotAllowed(methodNotAllowed),
	)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Body != nil {
			request.Body = http.MaxBytesReader(writer, request.Body, maximumRequestBodyBytes)
		}
		server.ServeHTTP(writer, request)
	}), nil
}

func writeProblem(writer http.ResponseWriter, status int, title string, detail string) error {
	response := struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	}
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	return json.NewEncoder(writer).Encode(response)
}
