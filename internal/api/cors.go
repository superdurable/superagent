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
	"log/slog"
	"net/http"
	"strings"

	"github.com/superdurable/superagent/internal/config"
)

const (
	allowedCORSHeaders = "Content-Type"
	allowedCORSMethods = "GET, POST"
	corsMaxAgeSeconds  = "600"
)

type corsHandler struct {
	next           http.Handler
	allowedOrigins map[config.Origin]struct{}
	logger         *slog.Logger
}

func newCORSHandler(next http.Handler, httpConfig *config.HTTP, logger *slog.Logger) http.Handler {
	if next == nil || httpConfig == nil || logger == nil {
		panic("CORS handler dependencies are required")
	}
	allowedOrigins := make(map[config.Origin]struct{}, len(httpConfig.AllowedOrigins))
	for _, origin := range httpConfig.AllowedOrigins {
		allowedOrigins[origin] = struct{}{}
	}
	return &corsHandler{next: next, allowedOrigins: allowedOrigins, logger: logger}
}

func (handler *corsHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		handler.next.ServeHTTP(writer, request)
		return
	}
	if len(origins) != 1 {
		handler.reject(writer, request, "exactly one Origin header is required")
		return
	}
	origin := config.Origin(origins[0])
	writer.Header().Add("Vary", "Origin")
	if _, allowed := handler.allowedOrigins[origin]; !allowed {
		handler.reject(writer, request, "the request origin is not allowed")
		return
	}

	writer.Header().Set("Access-Control-Allow-Origin", string(origin))
	if !isCORSPreflight(request) {
		handler.next.ServeHTTP(writer, request)
		return
	}
	if !supportsCORSMethod(request.Header.Get("Access-Control-Request-Method")) {
		handler.reject(writer, request, "the requested cross-origin method is not allowed")
		return
	}
	if !supportsCORSHeaders(request.Header.Get("Access-Control-Request-Headers")) {
		handler.reject(writer, request, "the requested cross-origin headers are not allowed")
		return
	}
	writer.Header().Set("Access-Control-Allow-Methods", allowedCORSMethods)
	writer.Header().Set("Access-Control-Allow-Headers", allowedCORSHeaders)
	writer.Header().Set("Access-Control-Max-Age", corsMaxAgeSeconds)
	writer.Header().Add("Vary", "Access-Control-Request-Method")
	writer.Header().Add("Vary", "Access-Control-Request-Headers")
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *corsHandler) reject(writer http.ResponseWriter, request *http.Request, detail string) {
	if err := writeProblem(writer, http.StatusForbidden, "Forbidden", detail); err != nil {
		handler.logger.ErrorContext(request.Context(), "write CORS rejection", slog.Any("error", err))
	}
}

func isCORSPreflight(request *http.Request) bool {
	return request.Method == http.MethodOptions && request.Header.Get("Access-Control-Request-Method") != ""
}

func supportsCORSMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodPost
}

func supportsCORSHeaders(headers string) bool {
	if strings.TrimSpace(headers) == "" {
		return true
	}
	for _, header := range strings.Split(headers, ",") {
		if !strings.EqualFold(strings.TrimSpace(header), "Content-Type") {
			return false
		}
	}
	return true
}
