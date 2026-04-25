// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ─────────────────────────────────────────────────────────────────────────────
// Classic Agent (v1 / Assistants API) — model types
// ─────────────────────────────────────────────────────────────────────────────

type ToolDefinition struct {
	Type string `json:"type"`
}

type CodeInterpreterResources struct {
	FileIDs []string `json:"file_ids,omitempty"`
}

type FileSearchResources struct {
	VectorStoreIDs []string `json:"vector_store_ids,omitempty"`
}

type ToolResources struct {
	CodeInterpreter *CodeInterpreterResources `json:"code_interpreter,omitempty"`
	FileSearch      *FileSearchResources      `json:"file_search,omitempty"`
}

type CreateAgentRequest struct {
	Model         string            `json:"model"`
	Name          string            `json:"name,omitempty"`
	Description   string            `json:"description,omitempty"`
	Instructions  string            `json:"instructions,omitempty"`
	Tools         []any             `json:"tools,omitempty"`
	ToolResources *ToolResources    `json:"tool_resources,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type AgentResponse struct {
	ID            string            `json:"id"`
	Object        string            `json:"object"`
	CreatedAt     int64             `json:"created_at"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Model         string            `json:"model"`
	Instructions  string            `json:"instructions"`
	Tools         []any             `json:"tools"`
	ToolResources *ToolResources    `json:"tool_resources"`
	Temperature   *float64          `json:"temperature"`
	TopP          *float64          `json:"top_p"`
	Metadata      map[string]string `json:"metadata"`
}

type UpdateAgentRequest struct {
	Model         string            `json:"model,omitempty"`
	Name          string            `json:"name,omitempty"`
	Description   string            `json:"description,omitempty"`
	Instructions  string            `json:"instructions,omitempty"`
	Tools         []any             `json:"tools,omitempty"`
	ToolResources *ToolResources    `json:"tool_resources,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type DeleteAgentResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Classic Agent CRUD (Assistants API)
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) CreateAgent(ctx context.Context, req CreateAgentRequest) (*AgentResponse, error) {
	url := fmt.Sprintf("%s/assistants?api-version=%s", c.ProjectEndpoint, APIVersion)
	return c.doAgentRequest(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) GetAgent(ctx context.Context, agentID string) (*AgentResponse, error) {
	url := fmt.Sprintf("%s/assistants/%s?api-version=%s", c.ProjectEndpoint, agentID, APIVersion)
	return c.doAgentRequest(ctx, http.MethodGet, url, nil)
}

func (c *FoundryClient) UpdateAgent(ctx context.Context, agentID string, req UpdateAgentRequest) (*AgentResponse, error) {
	url := fmt.Sprintf("%s/assistants/%s?api-version=%s", c.ProjectEndpoint, agentID, APIVersion)
	return c.doAgentRequest(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) DeleteAgent(ctx context.Context, agentID string) (*DeleteAgentResponse, error) {
	url := fmt.Sprintf("%s/assistants/%s?api-version=%s", c.ProjectEndpoint, agentID, APIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete agent HTTP error: %w", err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteAgentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete agent response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) doAgentRequest(ctx context.Context, method, url string, body any) (*AgentResponse, error) {
	httpReq, err := c.newRequest(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("agent API HTTP error (%s %s): %w", method, url, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result AgentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding agent response: %w", err)
	}
	return &result, nil
}
