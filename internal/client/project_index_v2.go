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
// Project Indexes — Foundry project data plane (api-version=v1).
//
// REST surface (matches the Python SDK's IndexesOperations on
// AIProjectClient):
//
//	PUT    {project}/indexes/{name}/versions/{version}?api-version=v1
//	GET    {project}/indexes/{name}/versions/{version}?api-version=v1
//	DELETE {project}/indexes/{name}/versions/{version}?api-version=v1
//
// Indexes are versioned (every CRUD takes a (name, version) tuple), but
// the Terraform resource hides that today and pins version="1" — most
// users register an index by name, they don't version-track it like an
// AzureML asset. Update is a merge-patch upsert against the same version.
//
// Polymorphic on `type`. Today supports `AzureSearch` (the
// AzureAISearchIndex SDK model). Future kinds documented by the SDK
// (CosmosDB, AzureBlob, …) follow the same outer envelope.
// ─────────────────────────────────────────────────────────────────────────────

// ProjectIndexAPIVersion is the api-version pinned for the project index
// data plane today. Treated as opaque: do not parse.
const ProjectIndexAPIVersion = "v1"

// ProjectIndexDefaultVersion is the version slug the Terraform resource
// uses when the user doesn't provide one. Indexes are technically
// versioned on the wire, but the resource flattens that into a
// "register this thing once, overwrite on update" model.
const ProjectIndexDefaultVersion = "1"

// IndexType discriminator values. Only AzureSearch is supported by this
// provider today; the SDK shape leaves room for more.
const ProjectIndexTypeAzureSearch = "AzureSearch"

// ProjectIndex is the wire envelope. The variant-specific fields
// (connectionName, indexName, fieldMapping) live FLAT on the same
// object as the discriminator — Foundry doesn't nest them under a
// per-type sub-object the way the agent tools do.
//
// Wire keys are camelCase per the Python SDK's rest_field annotations
// in azure-ai-projects/.../models/_models.py:
//
//	connection_name: str = rest_field(name="connectionName", …)
//	index_name:      str = rest_field(name="indexName", …)
//	field_mapping:   ... = rest_field(name="fieldMapping", …)
//
// v0.8.2 / v0.8.3 used snake_case JSON tags here; Foundry validated the
// flat envelope, didn't see connectionName / indexName, and returned 400
// "ConnectionName field is required". Issue #14 captured the verbatim
// 200-response shape — this struct now matches.
type ProjectIndex struct {
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Type           string            `json:"type"`
	ID             string            `json:"id,omitempty"`
	Description    string            `json:"description,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	ConnectionName string            `json:"connectionName,omitempty"`
	IndexName      string            `json:"indexName,omitempty"`
	FieldMapping   *FieldMapping     `json:"fieldMapping,omitempty"`
}

// FieldMapping is the optional column-rename envelope on AzureAISearchIndex.
// Empty pointer means "use the index's default schema". Specific fields
// stay strings even when they're nullable on the wire — the resource
// layer translates "" → null to keep the round-trip clean. JSON keys are
// camelCase per the SDK's rest_field annotations.
type FieldMapping struct {
	ContentFields  []string `json:"contentFields,omitempty"`
	FilepathField  string   `json:"filepathField,omitempty"`
	TitleField     string   `json:"titleField,omitempty"`
	URLField       string   `json:"urlField,omitempty"`
	VectorFields   []string `json:"vectorFields,omitempty"`
	MetadataFields []string `json:"metadataFields,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// CRUD
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) projectIndexURL(name, version string) string {
	return fmt.Sprintf("%s/indexes/%s/versions/%s?api-version=%s",
		c.ProjectEndpoint, url.PathEscape(name), url.PathEscape(version), ProjectIndexAPIVersion)
}

// CreateOrUpdateProjectIndex upserts a single (name, version) pair.
//
// Wire shape: HTTP **PATCH** with Content-Type
// `application/merge-patch+json` (RFC 7396). The Python SDK calls this
// `create_or_update` which sounds like PUT semantics, but the underlying
// transport is PATCH — verified against
// `azure-sdk-for-python/.../operations/_operations.py:795`
// (`build_indexes_create_or_update_request` → `HttpRequest(method="PATCH", …)`).
// Every call replaces the prior body wholesale, matching how Terraform
// Update wants to think about it.
//
// v0.8.2 issued PUT against the same URL and got HTTP 404 from the live
// service in swedencentral — see issue #12. The fix is the verb, nothing
// else: URL template, api-version, content-type were all already correct.
func (c *FoundryClient) CreateOrUpdateProjectIndex(ctx context.Context, idx ProjectIndex) (*ProjectIndex, error) {
	if idx.Version == "" {
		idx.Version = ProjectIndexDefaultVersion
	}
	target := c.projectIndexURL(idx.Name, idx.Version)
	httpReq, err := c.newRequest(ctx, http.MethodPatch, target, idx)
	if err != nil {
		return nil, err
	}
	// merge-patch+json signals an RFC 7396 partial-update upsert —
	// required by the service per the Python SDK's content-type default.
	httpReq.Header.Set("Content-Type", "application/merge-patch+json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("project index PATCH HTTP error (%s): %w", target, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result ProjectIndex
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding project index response: %w", err)
	}
	return &result, nil
}

// GetProjectIndex fetches a single (name, version) pair. 404 surfaces as
// a typed *APIError (StatusCode=404) so callers reuse the existing
// isNotFound helper.
func (c *FoundryClient) GetProjectIndex(ctx context.Context, name, version string) (*ProjectIndex, error) {
	if version == "" {
		version = ProjectIndexDefaultVersion
	}
	target := c.projectIndexURL(name, version)
	httpReq, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("project index GET HTTP error (%s): %w", target, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result ProjectIndex
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding project index GET response: %w", err)
	}
	return &result, nil
}

// DeleteProjectIndex returns nil for both 204 (deleted) and 404 (already
// gone) — the service contract says delete is idempotent.
func (c *FoundryClient) DeleteProjectIndex(ctx context.Context, name, version string) error {
	if version == "" {
		version = ProjectIndexDefaultVersion
	}
	target := c.projectIndexURL(name, version)
	httpReq, err := c.newRequest(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("project index DELETE HTTP error (%s): %w", target, err)
	}
	defer closeBody(resp)
	return checkResponseError(resp)
}
