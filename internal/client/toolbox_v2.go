// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ─────────────────────────────────────────────────────────────────────────────
// Toolbox v2 (/toolboxes) — model types
//
// REST surface (preview, pinned to api-version=v1):
//
//	POST   {project}/toolboxes/{name}/versions?api-version=v1
//	GET    {project}/toolboxes/{name}/versions?api-version=v1
//	GET    {project}/toolboxes/{name}/versions/{version}?api-version=v1
//	PATCH  {project}/toolboxes/{name}?api-version=v1   { "default_version": "..." }
//	DELETE {project}/toolboxes/{name}/versions/{version}?api-version=v1
//	GET    {project}/toolboxes/{name}?api-version=v1
//
// Toolbox creation is implicit: POSTing the first version of a name that
// doesn't exist auto-creates the parent ToolboxObject and promotes that
// version to default_version. Subsequent POSTs append immutable versions
// without changing default_version.
//
// The MCP consumer endpoints aren't management — they're consumed at agent
// runtime — so they're documented here but not exposed as Go methods:
//
//	{project}/toolboxes/{name}/mcp?api-version=v1                 (default)
//	{project}/toolboxes/{name}/versions/{version}/mcp?api-version=v1 (versioned)
//
// Both require the global Foundry-Features header which client.go sets on
// every request, so toolbox CRUD picks it up for free.
// ─────────────────────────────────────────────────────────────────────────────

// ToolboxAPIVersion is the api-version query string Foundry pins for the
// toolbox preview surface. Treated as opaque: do not parse.
const ToolboxAPIVersion = "v1"

// ToolboxObject is the parent container. default_version controls which
// version the consumer endpoint serves. The `id` field on the wire is
// opaque; the resource layer keys off `name` instead.
type ToolboxObject struct {
	Object         string `json:"object,omitempty"`
	ID             string `json:"id,omitempty"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	DefaultVersion string `json:"default_version,omitempty"`
	CreatedAt      int64  `json:"created_at,omitempty"`
}

// ToolboxVersionObject is an immutable snapshot of the toolbox's tool list.
// Tools is decoded as []any to mirror the agent_v2 dispatch model — typed
// shape lives in the resource layer where the toolExtractors/toolWirers
// maps already know how to round-trip each variant.
type ToolboxVersionObject struct {
	Object      string         `json:"object,omitempty"`
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description,omitempty"`
	CreatedAt   int64          `json:"created_at,omitempty"`
	Tools       []any          `json:"tools"`
	Policies    map[string]any `json:"policies,omitempty"`
}

// CreateToolboxVersionRequest is the body for POST /toolboxes/{name}/versions.
// Tools accepts the same wire shapes as AgentDefinitionV2.Tools (mcp,
// openapi, function, web_search, file_search, code_interpreter,
// azure_ai_search, …); Foundry validates the variant server-side.
type CreateToolboxVersionRequest struct {
	Description string `json:"description,omitempty"`
	Tools       []any  `json:"tools"`
}

// UpdateToolboxRequest is the body for PATCH /toolboxes/{name}. Foundry
// rejects an empty default_version, so callers must always supply one.
type UpdateToolboxRequest struct {
	DefaultVersion string `json:"default_version"`
}

// listToolboxVersionsResponse is the wire envelope for the list endpoint.
// Foundry returns an OpenAI-style {"data": [...]} envelope; we unwrap it
// before returning to callers.
type listToolboxVersionsResponse struct {
	Object string                 `json:"object,omitempty"`
	Data   []ToolboxVersionObject `json:"data"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Toolbox v2 CRUD
// ─────────────────────────────────────────────────────────────────────────────

// CreateToolboxVersion posts a new immutable version. The first version of
// a previously-unseen name auto-creates the parent ToolboxObject and is
// promoted to default_version. Subsequent versions do NOT auto-promote —
// callers must follow up with PromoteToolboxVersion to switch the default.
func (c *FoundryClient) CreateToolboxVersion(ctx context.Context, name string, req CreateToolboxVersionRequest) (*ToolboxVersionObject, error) {
	target := fmt.Sprintf("%s/toolboxes/%s/versions?api-version=%s", c.ProjectEndpoint, url.PathEscape(name), ToolboxAPIVersion)
	return c.doToolboxVersionRequest(ctx, http.MethodPost, target, req)
}

// GetToolboxVersion fetches a specific immutable version snapshot.
func (c *FoundryClient) GetToolboxVersion(ctx context.Context, name, version string) (*ToolboxVersionObject, error) {
	target := fmt.Sprintf("%s/toolboxes/%s/versions/%s?api-version=%s",
		c.ProjectEndpoint, url.PathEscape(name), url.PathEscape(version), ToolboxAPIVersion)
	return c.doToolboxVersionRequest(ctx, http.MethodGet, target, nil)
}

// ListToolboxVersions returns every version recorded for a toolbox.
// Order is server-defined; do not rely on it for "latest" semantics —
// use ToolboxObject.DefaultVersion instead.
func (c *FoundryClient) ListToolboxVersions(ctx context.Context, name string) ([]ToolboxVersionObject, error) {
	target := fmt.Sprintf("%s/toolboxes/%s/versions?api-version=%s", c.ProjectEndpoint, url.PathEscape(name), ToolboxAPIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("toolbox v2 list HTTP error (%s): %w", target, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var envelope listToolboxVersionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decoding toolbox v2 list response: %w", err)
	}
	return envelope.Data, nil
}

// GetToolbox fetches the parent container — primarily useful to read the
// current DefaultVersion before deciding whether to promote a new version.
func (c *FoundryClient) GetToolbox(ctx context.Context, name string) (*ToolboxObject, error) {
	target := fmt.Sprintf("%s/toolboxes/%s?api-version=%s", c.ProjectEndpoint, url.PathEscape(name), ToolboxAPIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("toolbox v2 get HTTP error (%s): %w", target, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result ToolboxObject
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding toolbox v2 get response: %w", err)
	}
	return &result, nil
}

// PromoteToolboxVersion sets the named version as the toolbox's
// default_version. Required after CreateToolboxVersion when the caller
// wants the new version live on the consumer endpoint — Foundry never
// auto-promotes on subsequent versions.
func (c *FoundryClient) PromoteToolboxVersion(ctx context.Context, name, version string) (*ToolboxObject, error) {
	target := fmt.Sprintf("%s/toolboxes/%s?api-version=%s", c.ProjectEndpoint, url.PathEscape(name), ToolboxAPIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodPatch, target, UpdateToolboxRequest{DefaultVersion: version})
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("toolbox v2 promote HTTP error (%s): %w", target, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result ToolboxObject
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding toolbox v2 promote response: %w", err)
	}
	return &result, nil
}

// DeleteToolboxVersion removes a single immutable version. Foundry refuses
// to delete the version currently set as default_version — callers must
// promote a different version first.
func (c *FoundryClient) DeleteToolboxVersion(ctx context.Context, name, version string) error {
	target := fmt.Sprintf("%s/toolboxes/%s/versions/%s?api-version=%s",
		c.ProjectEndpoint, url.PathEscape(name), url.PathEscape(version), ToolboxAPIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("toolbox v2 delete-version HTTP error (%s): %w", target, err)
	}
	defer closeBody(resp)
	return checkResponseError(resp)
}

// DeleteToolbox removes the parent ToolboxObject and every version it owns.
// The toolbox doc only documents per-version DELETE, but the parent DELETE
// matches the convention used elsewhere in the data plane and is what the
// Terraform resource needs to cleanly drop a managed toolbox. If Foundry
// rejects this with 405, callers should fall back to deleting all versions.
func (c *FoundryClient) DeleteToolbox(ctx context.Context, name string) error {
	target := fmt.Sprintf("%s/toolboxes/%s?api-version=%s", c.ProjectEndpoint, url.PathEscape(name), ToolboxAPIVersion)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("toolbox v2 delete HTTP error (%s): %w", target, err)
	}
	defer closeBody(resp)
	return checkResponseError(resp)
}

// ToolboxConsumerEndpoint returns the URL agents wire into an `mcp` tool
// block to consume a toolbox's promoted default version.
func (c *FoundryClient) ToolboxConsumerEndpoint(name string) string {
	return fmt.Sprintf("%s/toolboxes/%s/mcp?api-version=%s", c.ProjectEndpoint, url.PathEscape(name), ToolboxAPIVersion)
}

// ToolboxVersionedEndpoint returns the URL used to validate a specific
// version against an MCP client before promoting it.
func (c *FoundryClient) ToolboxVersionedEndpoint(name, version string) string {
	return fmt.Sprintf("%s/toolboxes/%s/versions/%s/mcp?api-version=%s",
		c.ProjectEndpoint, url.PathEscape(name), url.PathEscape(version), ToolboxAPIVersion)
}

func (c *FoundryClient) doToolboxVersionRequest(ctx context.Context, method, target string, body any) (*ToolboxVersionObject, error) {
	httpReq, err := c.newRequest(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("toolbox v2 API HTTP error (%s %s): %w", method, target, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result ToolboxVersionObject
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding toolbox v2 response: %w", err)
	}
	return &result, nil
}
