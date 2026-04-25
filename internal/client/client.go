// Copyright (c) Your Org
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const APIVersion = "2025-05-01"

// MemoryStoreAPIVersion pins the preview surface for Foundry Memory stores.
// Stays separate from APIVersion because the memory-store surface rev'd
// independently of the rest of the v2 API.
const MemoryStoreAPIVersion = "2025-11-15-preview"

type AuthMode int

const (
	AuthModeAzureCredential AuthMode = iota
	AuthModeAPIKey
)

// FoundryClient is an authenticated client for the Azure AI Foundry Agent Service.
type FoundryClient struct {
	ProjectEndpoint string
	authMode        AuthMode
	credential      azcore.TokenCredential
	apiKey          string
	httpClient      *http.Client

	// projectReady is set the first time a probe GET on the project's
	// data plane returns 2xx. Subsequent data-plane Creates short-circuit
	// the readiness wait so only the first resource per provider session
	// pays the propagation cost.
	projectReady   atomic.Bool
	projectReadyMu sync.Mutex
}

func NewFoundryClientWithCredential(projectEndpoint string, credential azcore.TokenCredential) *FoundryClient {
	return &FoundryClient{
		ProjectEndpoint: projectEndpoint,
		authMode:        AuthModeAzureCredential,
		credential:      credential,
		httpClient:      &http.Client{Timeout: 60 * time.Second},
	}
}

func NewFoundryClientWithAPIKey(projectEndpoint string, apiKey string) *FoundryClient {
	return &FoundryClient{
		ProjectEndpoint: projectEndpoint,
		authMode:        AuthModeAPIKey,
		apiKey:          apiKey,
		httpClient:      &http.Client{Timeout: 60 * time.Second},
	}
}

// WaitForProjectReady polls the Foundry data-plane endpoint until a cheap
// GET on /agents returns 2xx. This covers two intertwined startup races:
//
//  1. ARM finishes creating the project resource before the data-plane
//     project routing is ready (Foundry returns HTTP 404
//     "Project not found" until it is).
//  2. RBAC role assignments take 10–30 minutes to propagate to Foundry's
//     access-check cache. While they propagate, the data plane returns
//     401/403 even though the principal does have the role server-side.
//
// First-Create-per-session pays the wait; subsequent Creates short-circuit
// via the cached projectReady flag.
//
// timeout caps the total wait. Pass 0 to skip waiting (assume ready).
// Default suggestion for callers: 30 minutes.
func (c *FoundryClient) WaitForProjectReady(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	if c.projectReady.Load() {
		return nil
	}
	c.projectReadyMu.Lock()
	defer c.projectReadyMu.Unlock()
	if c.projectReady.Load() {
		return nil
	}

	url := fmt.Sprintf("%s/agents?api-version=v1", c.ProjectEndpoint)
	deadline := time.Now().Add(timeout)
	backoff := 5 * time.Second

	for {
		req, err := c.newRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("building project-readiness probe: %w", err)
		}
		resp, doErr := c.httpClient.Do(req)
		var status int
		if doErr == nil {
			status = resp.StatusCode
			resp.Body.Close()
			if status >= 200 && status < 300 {
				c.projectReady.Store(true)
				return nil
			}
		}
		if time.Now().After(deadline) {
			if doErr != nil {
				return fmt.Errorf("project at %s not reachable within %s: %w", c.ProjectEndpoint, timeout, doErr)
			}
			return fmt.Errorf("project at %s not reachable within %s: last status HTTP %d", c.ProjectEndpoint, timeout, status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 60*time.Second {
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Agent model types
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
	Tools         []interface{}     `json:"tools,omitempty"`
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
	Tools         []interface{}     `json:"tools"`
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
	Tools         []interface{}     `json:"tools,omitempty"`
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

// v2 - new API
type AgentDefinitionV2 struct {
    Kind             string                 `json:"kind"`
    Model            string                 `json:"model,omitempty"`
    Instructions     string                 `json:"instructions,omitempty"`
    Tools            []interface{}          `json:"tools,omitempty"`
    StructuredInputs map[string]interface{} `json:"structured_inputs,omitempty"`

    // Hosted-agent / container_app fields. Populated only when Kind is
    // "container_app" or "hosted"; omitted from the wire for "prompt" agents.
    Image                     string                 `json:"image,omitempty"`
    Cpu                       string                 `json:"cpu,omitempty"`
    Memory                    string                 `json:"memory,omitempty"`
    ContainerProtocolVersions []ProtocolVersionRecord `json:"container_protocol_versions,omitempty"`
    EnvironmentVariables      map[string]string      `json:"environment_variables,omitempty"`
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

type FileSearchToolV2 struct {
    Type           string   `json:"type"`
    VectorStoreIDs []string `json:"vector_store_ids,omitempty"`
    MaxNumResults  int      `json:"max_num_results,omitempty"`
}

// CodeInterpreterToolV2 — Foundry expects file_ids nested under container.
type CodeInterpreterContainer struct {
    Type    string   `json:"type"`              // "auto"
    FileIDs []string `json:"file_ids,omitempty"`
}

type CodeInterpreterToolV2 struct {
    Type      string                    `json:"type"`            // "code_interpreter"
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
    Type          string              `json:"type"`           // "bing_grounding"
    BingGrounding BingGroundingConfig `json:"bing_grounding"`
}

// FunctionToolV2 — OpenAI-style function calling. Parameters is a JSON Schema.
type FunctionToolV2 struct {
    Type        string                 `json:"type"` // "function"
    Name        string                 `json:"name"`
    Description string                 `json:"description,omitempty"`
    Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// OpenAPIToolV2 — inline OpenAPI spec.
type OpenAPIAuth struct {
    Type string `json:"type"` // anonymous | connection
}

type OpenAPIConfig struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description,omitempty"`
    Spec        map[string]interface{} `json:"spec"`
    Auth        OpenAPIAuth            `json:"auth"`
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
    Type           string              `json:"type"` // "azure_ai_search"
    AzureAISearch  AzureAISearchConfig `json:"azure_ai_search"`
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
// Memory store model types
// ─────────────────────────────────────────────────────────────────────────────

type MemoryStoreOptions struct {
    UserProfileEnabled  bool   `json:"user_profile_enabled,omitempty"`
    ChatSummaryEnabled  bool   `json:"chat_summary_enabled,omitempty"`
    UserProfileDetails  string `json:"user_profile_details,omitempty"`
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
// File model types
// ─────────────────────────────────────────────────────────────────────────────

type FilePurpose string

const FilePurposeAssistants FilePurpose = "assistants"

type FileResponse struct {
	ID        string      `json:"id"`
	Object    string      `json:"object"`
	Bytes     int64       `json:"bytes"`
	CreatedAt int64       `json:"created_at"`
	Filename  string      `json:"filename"`
	Purpose   FilePurpose `json:"purpose"`
}

type DeleteFileResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Vector store model types
// ─────────────────────────────────────────────────────────────────────────────

type VectorStoreStatus string

const (
	VectorStoreStatusInProgress VectorStoreStatus = "in_progress"
	VectorStoreStatusCompleted  VectorStoreStatus = "completed"
	VectorStoreStatusExpired    VectorStoreStatus = "expired"
)

type VectorStoreExpirationPolicy struct {
	Anchor string `json:"anchor,omitempty"`
	Days   int64  `json:"days,omitempty"`
}

type VectorStoreFileCounts struct {
	InProgress int64 `json:"in_progress"`
	Completed  int64 `json:"completed"`
	Failed     int64 `json:"failed"`
	Cancelled  int64 `json:"cancelled"`
	Total      int64 `json:"total"`
}

type VectorStoreResponse struct {
	ID           string                       `json:"id"`
	Object       string                       `json:"object"`
	CreatedAt    int64                        `json:"created_at"`
	Name         string                       `json:"name"`
	UsageBytes   int64                        `json:"usage_bytes"`
	FileCounts   VectorStoreFileCounts        `json:"file_counts"`
	Status       VectorStoreStatus            `json:"status"`
	ExpiresAfter *VectorStoreExpirationPolicy `json:"expires_after,omitempty"`
	ExpiresAt    *int64                       `json:"expires_at,omitempty"`
	LastActiveAt int64                        `json:"last_active_at"`
	Metadata     map[string]string            `json:"metadata"`
}

type CreateVectorStoreRequest struct {
	Name         string                       `json:"name,omitempty"`
	FileIDs      []string                     `json:"file_ids,omitempty"`
	ExpiresAfter *VectorStoreExpirationPolicy `json:"expires_after,omitempty"`
	Metadata     map[string]string            `json:"metadata,omitempty"`
}

type UpdateVectorStoreRequest struct {
	Name         string                       `json:"name,omitempty"`
	ExpiresAfter *VectorStoreExpirationPolicy `json:"expires_after,omitempty"`
	Metadata     map[string]string            `json:"metadata,omitempty"`
}

type DeleteVectorStoreResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Agent CRUD
// ─────────────────────────────────────────────────────────────────────────────


// classic foundry hub API
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
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteAgentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete agent response: %w", err)
	}
	return &result, nil
}

// new CRUD functions, pointing at the newer /agents Microsoft Foundry API
func (c *FoundryClient) CreateAgentV2(ctx context.Context, req CreateAgentV2Request) (*AgentResponseV2, error) {
    url := fmt.Sprintf("%s/agents?api-version=v1", c.ProjectEndpoint)
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
    defer resp.Body.Close()
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
			resp.Body.Close()
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

// ─────────────────────────────────────────────────────────────────────────────
// File CRUD
// ─────────────────────────────────────────────────────────────────────────────

// UploadFile uploads file contents with the given filename.
// Use FilePurposeAssistants for files used with file_search or code_interpreter.
func (c *FoundryClient) UploadFile(ctx context.Context, filename string, fileData []byte, purpose FilePurpose) (*FileResponse, error) {
	url := fmt.Sprintf("%s/files?api-version=%s", c.ProjectEndpoint, APIVersion)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if err := mw.WriteField("purpose", string(purpose)); err != nil {
		return nil, fmt.Errorf("writing purpose field: %w", err)
	}

	part, err := mw.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return nil, fmt.Errorf("creating form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return nil, fmt.Errorf("writing file data: %w", err)
	}
	mw.Close()

	httpReq, err := c.newRequestRaw(ctx, http.MethodPost, url, &buf, mw.FormDataContentType())
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upload file HTTP error: %w", err)
	}
	defer resp.Body.Close()

	if err := checkResponseError(resp); err != nil {
		return nil, err
	}

	var result FileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding upload file response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) GetFile(ctx context.Context, fileID string) (*FileResponse, error) {
	url := fmt.Sprintf("%s/files/%s?api-version=%s", c.ProjectEndpoint, fileID, APIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("get file HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result FileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding get file response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) DeleteFile(ctx context.Context, fileID string) (*DeleteFileResponse, error) {
	url := fmt.Sprintf("%s/files/%s?api-version=%s", c.ProjectEndpoint, fileID, APIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete file HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete file response: %w", err)
	}
	return &result, nil
}

// v2
func (c *FoundryClient) UploadFileV2(ctx context.Context, filename string, fileData []byte, purpose FilePurpose) (*FileResponse, error) {
    url := fmt.Sprintf("%s/files?api-version=v1", c.ProjectEndpoint)

    var buf bytes.Buffer
    mw := multipart.NewWriter(&buf)

    if err := mw.WriteField("purpose", string(purpose)); err != nil {
        return nil, fmt.Errorf("writing purpose field: %w", err)
    }

    part, err := mw.CreateFormFile("file", filepath.Base(filename))
    if err != nil {
        return nil, fmt.Errorf("creating form file: %w", err)
    }
    if _, err := part.Write(fileData); err != nil {
        return nil, fmt.Errorf("writing file data: %w", err)
    }
    mw.Close()

    httpReq, err := c.newRequestRaw(ctx, http.MethodPost, url, &buf, mw.FormDataContentType())
    if err != nil {
        return nil, err
    }

    resp, err := c.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("upload file v2 HTTP error: %w", err)
    }
    defer resp.Body.Close()

    if err := checkResponseError(resp); err != nil {
        return nil, err
    }

    var result FileResponse
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, fmt.Errorf("decoding upload file v2 response: %w", err)
    }
    return &result, nil
}

func (c *FoundryClient) GetFileV2(ctx context.Context, fileID string) (*FileResponse, error) {
    url := fmt.Sprintf("%s/files/%s?api-version=v1", c.ProjectEndpoint, fileID)
    httpReq, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("get file HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result FileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding get file response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) DeleteFileV2(ctx context.Context, fileID string) (*DeleteFileResponse, error) {
    url := fmt.Sprintf("%s/files/%s?api-version=v1", c.ProjectEndpoint, fileID)
    httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete file HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete file response: %w", err)
	}
	return &result, nil
}



// ─────────────────────────────────────────────────────────────────────────────
// Vector store CRUD
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) CreateVectorStore(ctx context.Context, req CreateVectorStoreRequest) (*VectorStoreResponse, error) {
	url := fmt.Sprintf("%s/vector_stores?api-version=%s", c.ProjectEndpoint, APIVersion)
	return c.doVectorStoreRequest(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) GetVectorStore(ctx context.Context, vectorStoreID string) (*VectorStoreResponse, error) {
	url := fmt.Sprintf("%s/vector_stores/%s?api-version=%s", c.ProjectEndpoint, vectorStoreID, APIVersion)
	return c.doVectorStoreRequest(ctx, http.MethodGet, url, nil)
}

func (c *FoundryClient) UpdateVectorStore(ctx context.Context, vectorStoreID string, req UpdateVectorStoreRequest) (*VectorStoreResponse, error) {
	url := fmt.Sprintf("%s/vector_stores/%s?api-version=%s", c.ProjectEndpoint, vectorStoreID, APIVersion)
	return c.doVectorStoreRequest(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) DeleteVectorStore(ctx context.Context, vectorStoreID string) (*DeleteVectorStoreResponse, error) {
	url := fmt.Sprintf("%s/vector_stores/%s?api-version=%s", c.ProjectEndpoint, vectorStoreID, APIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete vector store HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteVectorStoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete vector store response: %w", err)
	}
	return &result, nil
}

// WaitForVectorStore polls until the vector store status is "completed" or "expired".
func (c *FoundryClient) WaitForVectorStore(ctx context.Context, vectorStoreID string) (*VectorStoreResponse, error) {
	for {
		vs, err := c.GetVectorStore(ctx, vectorStoreID)
		if err != nil {
			return nil, err
		}
		switch vs.Status {
		case VectorStoreStatusCompleted:
			return vs, nil
		case VectorStoreStatusExpired:
			return nil, fmt.Errorf("vector store %s expired before completing ingestion", vectorStoreID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

//v2

func (c *FoundryClient) CreateVectorStoreV2(ctx context.Context, req CreateVectorStoreRequest) (*VectorStoreResponse, error) {
    url := fmt.Sprintf("%s/vector_stores?api-version=v1", c.ProjectEndpoint)
    return c.doVectorStoreRequest(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) GetVectorStoreV2(ctx context.Context, vectorStoreID string) (*VectorStoreResponse, error) {
    url := fmt.Sprintf("%s/vector_stores/%s?api-version=v1", c.ProjectEndpoint, vectorStoreID)
    return c.doVectorStoreRequest(ctx, http.MethodGet, url, nil)
}

func (c *FoundryClient) UpdateVectorStoreV2(ctx context.Context, vectorStoreID string, req UpdateVectorStoreRequest) (*VectorStoreResponse, error) {
    url := fmt.Sprintf("%s/vector_stores/%s?api-version=v1", c.ProjectEndpoint, vectorStoreID)
    return c.doVectorStoreRequest(ctx, http.MethodPost, url, req)
}

func (c *FoundryClient) DeleteVectorStoreV2(ctx context.Context, vectorStoreID string) (*DeleteVectorStoreResponse, error) {
    url := fmt.Sprintf("%s/vector_stores/%s?api-version=v1", c.ProjectEndpoint, vectorStoreID)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete vector store HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteVectorStoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete vector store response: %w", err)
	}
	return &result, nil
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
    defer resp.Body.Close()
    if err := checkResponseError(resp); err != nil {
        return nil, err
    }
    var result DeleteMemoryStoreResponse
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, fmt.Errorf("decoding delete memory store response: %w", err)
    }
    return &result, nil
}

func (c *FoundryClient) doMemoryStoreRequest(ctx context.Context, method, url string, body interface{}) (*MemoryStoreResponse, error) {
    httpReq, err := c.newRequest(ctx, method, url, body)
    if err != nil {
        return nil, err
    }
    resp, err := c.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("memory store API HTTP error (%s %s): %w", method, url, err)
    }
    defer resp.Body.Close()
    if err := checkResponseError(resp); err != nil {
        return nil, err
    }
    var result MemoryStoreResponse
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, fmt.Errorf("decoding memory store response: %w", err)
    }
    return &result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) doAgentRequest(ctx context.Context, method, url string, body interface{}) (*AgentResponse, error) {
	httpReq, err := c.newRequest(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("agent API HTTP error (%s %s): %w", method, url, err)
	}
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result AgentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding agent response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) doVectorStoreRequest(ctx context.Context, method, url string, body interface{}) (*VectorStoreResponse, error) {
	httpReq, err := c.newRequest(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vector store API HTTP error (%s %s): %w", method, url, err)
	}
	defer resp.Body.Close()
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result VectorStoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding vector store response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) newRequest(ctx context.Context, method, url string, body interface{}) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshalling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	return c.newRequestRaw(ctx, method, url, bodyReader, "application/json")
}

func (c *FoundryClient) newRequestRaw(ctx context.Context, method, url string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	switch c.authMode {
	case AuthModeAPIKey:
		req.Header.Set("api-key", c.apiKey)
	case AuthModeAzureCredential:
		tokenOpts := policy.TokenRequestOptions{
			Scopes: []string{"https://ai.azure.com/.default"},
		}
		token, err := c.credential.GetToken(ctx, tokenOpts)
		if err != nil {
			return nil, fmt.Errorf("acquiring Azure token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.Token)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	// Foundry preview opt-in. Harmless on non-preview endpoints; required
	// for hosted-agent CRUD (HTTP 403 preview_feature_required without it)
	// and for memory-store CRUD (HTTP 404 "Project not found" — the API
	// lies about which feature is missing). Comma-separated list.
	req.Header.Set("Foundry-Features", "HostedAgents=V1Preview, MemoryStores=V1Preview")
	return req, nil
}

type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Azure AI Foundry API error (HTTP %d): %s", e.StatusCode, e.Body)
}

func checkResponseError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return &APIError{StatusCode: resp.StatusCode, Body: string(body)}
}

// v2
func (c *FoundryClient) doAgentV2Request(ctx context.Context, method, url string, body interface{}) (*AgentResponseV2, error) {
    httpReq, err := c.newRequest(ctx, method, url, body)
    if err != nil {
        return nil, err
    }
    resp, err := c.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("agent v2 API HTTP error (%s %s): %w", method, url, err)
    }
    defer resp.Body.Close()
    if err := checkResponseError(resp); err != nil {
        return nil, err
    }
    var result AgentResponseV2
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, fmt.Errorf("decoding agent v2 response: %w", err)
    }
    return &result, nil
}