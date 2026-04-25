// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// MemoryStoreAPIVersion pins the preview surface for Foundry Memory stores.
// Stays separate from APIVersion because the memory-store surface rev'd
// independently of the rest of the v2 API.
const MemoryStoreAPIVersion = "2025-11-15-preview"

// ─────────────────────────────────────────────────────────────────────────────
// Memory store — model types (v2 preview)
// ─────────────────────────────────────────────────────────────────────────────

type MemoryStoreOptions struct {
	UserProfileEnabled bool   `json:"user_profile_enabled,omitempty"`
	ChatSummaryEnabled bool   `json:"chat_summary_enabled,omitempty"`
	UserProfileDetails string `json:"user_profile_details,omitempty"`
}

// MemoryStoreDefinition mirrors the Python SDK MemoryStoreDefaultDefinition.
// Kind is fixed to "default" for now — it's the only shape Foundry accepts
// during preview.
type MemoryStoreDefinition struct {
	Kind           string              `json:"kind"` // "default"
	ChatModel      string              `json:"chat_model"`
	EmbeddingModel string              `json:"embedding_model"`
	Options        *MemoryStoreOptions `json:"options,omitempty"`
}

type MemoryStoreResponse struct {
	Object      string                `json:"object"`
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	CreatedAt   int64                 `json:"created_at"`
	Definition  MemoryStoreDefinition `json:"definition"`
	Metadata    map[string]string     `json:"metadata,omitempty"`
}

type CreateMemoryStoreRequest struct {
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Definition  MemoryStoreDefinition `json:"definition"`
	Metadata    map[string]string     `json:"metadata,omitempty"`
}

type UpdateMemoryStoreRequest struct {
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type DeleteMemoryStoreResponse struct {
	Object  string `json:"object"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Memory store CRUD (preview)
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) CreateMemoryStore(ctx context.Context, req CreateMemoryStoreRequest) (*MemoryStoreResponse, error) {
	url := fmt.Sprintf("%s/memory_stores?api-version=%s", c.ProjectEndpoint, MemoryStoreAPIVersion)
	return c.doMemoryStoreRequest(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) GetMemoryStore(ctx context.Context, name string) (*MemoryStoreResponse, error) {
	url := fmt.Sprintf("%s/memory_stores/%s?api-version=%s", c.ProjectEndpoint, name, MemoryStoreAPIVersion)
	return c.doMemoryStoreRequest(ctx, http.MethodGet, url, nil)
}

func (c *FoundryClient) UpdateMemoryStore(ctx context.Context, name string, req UpdateMemoryStoreRequest) (*MemoryStoreResponse, error) {
	url := fmt.Sprintf("%s/memory_stores/%s?api-version=%s", c.ProjectEndpoint, name, MemoryStoreAPIVersion)
	return c.doMemoryStoreRequest(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) DeleteMemoryStore(ctx context.Context, name string) (*DeleteMemoryStoreResponse, error) {
	url := fmt.Sprintf("%s/memory_stores/%s?api-version=%s", c.ProjectEndpoint, name, MemoryStoreAPIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete memory store HTTP error: %w", err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteMemoryStoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete memory store response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) doMemoryStoreRequest(ctx context.Context, method, url string, body any) (*MemoryStoreResponse, error) {
	httpReq, err := c.newRequest(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("memory store API HTTP error (%s %s): %w", method, url, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result MemoryStoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding memory store response: %w", err)
	}
	return &result, nil
}
