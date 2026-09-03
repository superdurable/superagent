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
	"errors"
	"strings"
	"sync"

	"github.com/superdurable/superagent/internal/agent"
)

// CredentialStore keeps provider keys only in Worker process memory.
type CredentialStore struct {
	mutex    sync.RWMutex
	keys     map[credentialKey]string
	defaults map[agent.Provider]string
}

type credentialKey struct {
	flowID   agent.FlowID
	provider agent.Provider
}

// NewCredentialStore creates an empty in-memory store.
func NewCredentialStore() *CredentialStore {
	return &CredentialStore{
		keys:     make(map[credentialKey]string),
		defaults: make(map[agent.Provider]string),
	}
}

// SetDefaultAPIKey sets the process-level key used after Worker replacement and for new Flows.
func (store *CredentialStore) SetDefaultAPIKey(provider agent.Provider, apiKey string) error {
	if err := provider.Validate(); err != nil {
		return err
	}
	if err := validateAPIKey(apiKey); err != nil {
		return err
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if apiKey == "" {
		delete(store.defaults, provider)
		return nil
	}
	store.defaults[provider] = apiKey
	return nil
}

// SetAPIKey associates a key with one Flow and provider.
func (store *CredentialStore) SetAPIKey(flowID agent.FlowID, provider agent.Provider, apiKey string) error {
	if strings.TrimSpace(string(flowID)) == "" {
		return errors.New("flow ID must not be empty")
	}
	if err := provider.Validate(); err != nil {
		return err
	}
	if err := validateAPIKey(apiKey); err != nil {
		return err
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	key := credentialKey{flowID: flowID, provider: provider}
	if apiKey == "" {
		delete(store.keys, key)
		return nil
	}
	store.keys[key] = apiKey
	return nil
}

// APIKey returns the key for one Flow and provider.
func (store *CredentialStore) APIKey(flowID agent.FlowID, provider agent.Provider) string {
	store.mutex.RLock()
	defer store.mutex.RUnlock()
	if apiKey := store.keys[credentialKey{flowID: flowID, provider: provider}]; apiKey != "" {
		return apiKey
	}
	return store.defaults[provider]
}

// HasAPIKey reports whether a Flow-specific or process-level key is available.
func (store *CredentialStore) HasAPIKey(flowID agent.FlowID, provider agent.Provider) bool {
	return store.APIKey(flowID, provider) != ""
}

// DeleteFlow removes every key associated with one Flow.
func (store *CredentialStore) DeleteFlow(flowID agent.FlowID) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	for key := range store.keys {
		if key.flowID == flowID {
			delete(store.keys, key)
		}
	}
}

func validateAPIKey(apiKey string) error {
	for _, character := range apiKey {
		if character < 0x20 || character > 0x7e {
			return errors.New("API key must contain only printable ASCII characters")
		}
	}
	return nil
}
