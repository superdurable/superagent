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

// Package webui serves the embedded Phase 1 React application.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
)

const (
	pagePath              = "/products/ai-agent/"
	assetPath             = "/products/ai-agent/assets/"
	contentSecurityPolicy = "default-src 'none'; connect-src 'self'; img-src 'self'; script-src 'self'; style-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
)

type assetName string

const (
	assetIndex      assetName = "index.html"
	assetBundle     assetName = "bundle.js"
	assetStylesheet assetName = "styles.css"
)

//go:embed assets/index.html assets/bundle.js assets/styles.css
var assetFiles embed.FS

var (
	indexAsset      = mustAsset(assetIndex, "text/html; charset=utf-8")
	bundleAsset     = mustAsset(assetBundle, "text/javascript; charset=utf-8")
	stylesheetAsset = mustAsset(assetStylesheet, "text/css; charset=utf-8")
)

type embeddedAsset struct {
	name        string
	contentType string
	contents    []byte
	etag        string
}

// NewHandler routes UI files locally and delegates every API route to ogen.
func NewHandler(api http.Handler) http.Handler {
	if api == nil {
		panic("OpenAPI handler is required")
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var selected *embeddedAsset
		switch request.URL.Path {
		case pagePath:
			selected = &indexAsset
		case assetPath + string(assetBundle):
			selected = &bundleAsset
		case assetPath + string(assetStylesheet):
			selected = &stylesheetAsset
		default:
			api.ServeHTTP(writer, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		setSecurityHeaders(writer.Header())
		writer.Header().Set("Content-Type", selected.contentType)
		writer.Header().Set("ETag", selected.etag)
		if selected.name == string(assetIndex) {
			writer.Header().Set("Cache-Control", "no-store")
		} else {
			writer.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeContent(writer, request, selected.name, time.Time{}, bytes.NewReader(selected.contents))
	})
}

func mustAsset(name assetName, contentType string) embeddedAsset {
	contents, err := assetFiles.ReadFile("assets/" + string(name))
	if err != nil {
		panic(fmt.Sprintf("read embedded UI asset %q: %v", name, err))
	}
	digest := sha256.Sum256(contents)
	return embeddedAsset{
		name:        string(name),
		contentType: contentType,
		contents:    contents,
		etag:        `"` + hex.EncodeToString(digest[:]) + `"`,
	}
}

func setSecurityHeaders(headers http.Header) {
	headers.Set("Content-Security-Policy", contentSecurityPolicy)
	headers.Set("Referrer-Policy", "no-referrer")
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("X-Frame-Options", "DENY")
}
