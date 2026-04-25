// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Agent v2 (/agents) — model types
// ─────────────────────────────────────────────────────────────────────────────

type AgentDefinitionV2 struct {
	Kind             string         `json:"kind"`
	Model            string         `json:"model,omitempty"`
	Instructions     string         `json:"instructions,omitempty"`
	Tools            []any          `json:"tools,omitempty"`
	StructuredInputs map[string]any `json:"structured_inputs,omitempty"`

	// Hosted-agent / container_app fields. Populated only when Kind is
	// "container_app" or "hosted"; omitted from the wire for "prompt" agents.
	Image                     string                  `json:"image,omitempty"`
	CPU                       string                  `json:"cpu,omitempty"`
	Memory                    string                  `json:"memory,omitempty"`
	ContainerProtocolVersions []ProtocolVersionRecord `json:"container_protocol_versions,omitempty"`
	EnvironmentVariables      map[string]string       `json:"environment_variables,omitempty"`
}

// ProtocolVersionRecord matches the Foundry HostedAgentDefinition protocol
// envelope. Allowed protocols as of 2026-04: "responses", "a2a".
type ProtocolVersionRecord struct {
	Protocol string `json:"protocol"`
	Version  string `json:"version"`
}

// AgentInstanceIdentity is the Foundry-managed Entra identity attached to a
// hosted agent version. Foundry assigns one per version at create time and
// the platform uses it for runtime model + tool access. Exposed as an output
// so Pulumi/Terraform programs can grant RBAC (e.g. Azure AI User) to it.
type AgentInstanceIdentity struct {
	ClientID    string `json:"client_id"`
	PrincipalID string `json:"principal_id"`
}

type AgentVersionV2 struct {
	Object           string                 `json:"object"`
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Version          string                 `json:"version"`
	Description      string                 `json:"description"`
	CreatedAt        int64                  `json:"created_at"`
	Metadata         map[string]string      `json:"metadata"`
	Definition       AgentDefinitionV2      `json:"definition"`
	InstanceIdentity *AgentInstanceIdentity `json:"instance_identity,omitempty"`
}

type AgentResponseV2 struct {
	Object   string `json:"object"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Versions struct {
		Latest AgentVersionV2 `json:"latest"`
	} `json:"versions"`
}

type CreateAgentV2Request struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Definition  AgentDefinitionV2 `json:"definition"`
}

type UpdateAgentV2Request struct {
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Definition  AgentDefinitionV2 `json:"definition"`
}

type DeleteAgentV2Response struct {
	Object  string `json:"object"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

// ─────────────────────────────────────────────────────────────────────────────
// v2 Tool wire types
// ─────────────────────────────────────────────────────────────────────────────

type FileSearchToolV2 struct {
	Type           string   `json:"type"`
	VectorStoreIDs []string `json:"vector_store_ids,omitempty"`
	MaxNumResults  int      `json:"max_num_results,omitempty"`
}

// CodeInterpreterToolV2 — Foundry expects file_ids nested under container.
type CodeInterpreterContainer struct {
	Type    string   `json:"type"` // "auto"
	FileIDs []string `json:"file_ids,omitempty"`
}

type CodeInterpreterToolV2 struct {
	Type      string                    `json:"type"` // "code_interpreter"
	Container *CodeInterpreterContainer `json:"container,omitempty"`
}

// WebSearchToolV2 — managed Bing-via-Foundry. No connection needed.
type WebSearchToolV2 struct {
	Type string `json:"type"` // "web_search"
}

// BingGroundingToolV2 — Bing Search v7 via a project connection.
type BingGroundingConfig struct {
	ConnectionID string `json:"connection_id"`
}

type BingGroundingToolV2 struct {
	Type          string              `json:"type"` // "bing_grounding"
	BingGrounding BingGroundingConfig `json:"bing_grounding"`
}

// FunctionToolV2 — OpenAI-style function calling. Parameters is a JSON Schema.
type FunctionToolV2 struct {
	Type        string         `json:"type"` // "function"
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// OpenAPIToolV2 — inline OpenAPI spec.
type OpenAPIAuth struct {
	Type string `json:"type"` // anonymous | connection
}

type OpenAPIConfig struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Spec        map[string]any `json:"spec"`
	Auth        OpenAPIAuth    `json:"auth"`
}

type OpenAPIToolV2 struct {
	Type    string        `json:"type"` // "openapi"
	OpenAPI OpenAPIConfig `json:"openapi"`
}

// MCPToolV2 — Model Context Protocol server.
type MCPToolV2 struct {
	Type                string `json:"type"` // "mcp"
	ServerLabel         string `json:"server_label"`
	ServerURL           string `json:"server_url"`
	RequireApproval     string `json:"require_approval,omitempty"`
	ProjectConnectionID string `json:"project_connection_id,omitempty"`
}

// AzureAISearchToolV2 — Azure AI Search via project connection + index.
type AzureAISearchIndex struct {
	ProjectConnectionID string `json:"project_connection_id"`
	IndexName           string `json:"index_name"`
	QueryType           string `json:"query_type,omitempty"`
	TopK                int    `json:"top_k,omitempty"`
}

type AzureAISearchConfig struct {
	Indexes []AzureAISearchIndex `json:"indexes"`
}

type AzureAISearchToolV2 struct {
	Type          string              `json:"type"` // "azure_ai_search"
	AzureAISearch AzureAISearchConfig `json:"azure_ai_search"`
}

// MemorySearchToolV2 — attaches a Foundry Memory store to the agent. The
// Foundry wire type is "memory_search_preview" while in preview; we keep the
// user-facing schema type "memory_search" for forward-compat and translate.
type MemorySearchToolV2 struct {
	Type            string `json:"type"` // "memory_search_preview"
	MemoryStoreName string `json:"memory_store_name"`
	Scope           string `json:"scope,omitempty"`
	UpdateDelay     int    `json:"update_delay,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Agent v2 CRUD
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) CreateAgentV2(ctx context.Context, req CreateAgentV2Request) (*AgentResponseV2, error) {
	url := c.ProjectEndpoint + "/agents?api-version=v1"
	return c.doAgentV2Request(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) GetAgentV2(ctx context.Context, name string) (*AgentResponseV2, error) {
	url := fmt.Sprintf("%s/agents/%s?api-version=v1", c.ProjectEndpoint, name)
	return c.doAgentV2Request(ctx, http.MethodGet, url, nil)
}

func (c *FoundryClient) UpdateAgentV2(ctx context.Context, name string, req UpdateAgentV2Request) (*AgentResponseV2, error) {
	url := fmt.Sprintf("%s/agents/%s?api-version=v1", c.ProjectEndpoint, name)
	return c.doAgentV2Request(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) DeleteAgentV2(ctx context.Context, name string) (*DeleteAgentV2Response, error) {
	url := fmt.Sprintf("%s/agents/%s?api-version=v1", c.ProjectEndpoint, name)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete agent v2 HTTP error: %w", err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteAgentV2Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete agent v2 response: %w", err)
	}
	return &result, nil
}

// WaitForAgentV2Ready polls the agent's Responses endpoint until the
// per-session sandbox is warm enough to accept traffic. Foundry returns
// HTTP 424 ("session_not_ready") while the sandbox is cold-starting; once
// the agent route is up, a GET against the (POST-only) Responses URL
// returns 405 ("Method Not Allowed") — that's our positive readiness
// signal. Any other 2xx/3xx/4xx (except 424) is treated as ready since
// it means the route reached the agent. 5xx and transport errors are
// retried up to the timeout.
//
// pollInterval is clamped to a 1s minimum to avoid hammering the API.
// Only meaningful for kind="hosted" / "container_app" agents. Calling
// this on a kind="prompt" agent returns nil immediately because the
// Responses endpoint isn't agent-routed for prompt agents.
func (c *FoundryClient) WaitForAgentV2Ready(ctx context.Context, name string, timeout, pollInterval time.Duration) error {
	if pollInterval < time.Second {
		pollInterval = time.Second
	}
	url := fmt.Sprintf("%s/agents/%s/endpoint/protocols/responses/v1/responses", c.ProjectEndpoint, name)
	deadline := time.Now().Add(timeout)
	for {
		httpReq, err := c.newRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("building readiness probe: %w", err)
		}
		resp, err := c.httpClient.Do(httpReq)
		if err == nil {
			status := resp.StatusCode
			closeBody(resp)
			// 424 is the documented "session_not_ready" — keep polling.
			// 5xx are transient — keep polling.
			// Anything else means the agent route is up.
			if status != http.StatusFailedDependency && (status < 500 || status >= 600) {
				return nil
			}
		}
		// network error or 424/5xx — retry until the deadline.
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("agent %q did not become ready within %s: last error: %w", name, timeout, err)
			}
			return fmt.Errorf("agent %q did not become ready within %s: still returning HTTP 424 / 5xx", name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (c *FoundryClient) doAgentV2Request(ctx context.Context, method, url string, body any) (*AgentResponseV2, error) {
	httpReq, err := c.newRequest(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("agent v2 API HTTP error (%s %s): %w", method, url, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result AgentResponseV2
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding agent v2 response: %w", err)
	}
	return &result, nil
}
