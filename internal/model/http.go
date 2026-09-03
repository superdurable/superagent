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

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maximumProviderResponseBytes = 16 << 20

type providerHTTPError struct {
	provider   string
	statusCode int
}

func (err *providerHTTPError) Error() string {
	return fmt.Sprintf("%s API returned HTTP %d", err.provider, err.statusCode)
}

func parseProviderBaseURL(provider string, rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse %s base URL: %w", provider, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s base URL must be an absolute HTTPS origin without credentials, query, or fragment", provider)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, nil
}

func providerURL(base *url.URL, path string) string {
	copyURL := *base
	copyURL.Path = strings.TrimRight(copyURL.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return copyURL.String()
}

func doProviderJSON(
	ctx context.Context,
	httpClient *http.Client,
	provider string,
	endpoint string,
	headers http.Header,
	requestBody any,
	responseBody any,
) error {
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", provider, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create %s request: %w", provider, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send %s request: %w", provider, err)
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("%s API returned an empty HTTP response", provider)
	}
	defer func() { _ = response.Body.Close() }()
	limited := io.LimitReader(response.Body, maximumProviderResponseBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read %s response: %w", provider, err)
	}
	if len(payload) > maximumProviderResponseBytes {
		return fmt.Errorf("%s response exceeds %d bytes", provider, maximumProviderResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &providerHTTPError{provider: provider, statusCode: response.StatusCode}
	}
	if responseBody == nil {
		return errors.New("provider response target is required")
	}
	if err := json.Unmarshal(payload, responseBody); err != nil {
		return fmt.Errorf("decode %s response: %w", provider, err)
	}
	return nil
}
