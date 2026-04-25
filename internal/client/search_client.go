// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// ─────────────────────────────────────────────────────────────────────────────
// SearchClient — Azure AI Search data plane (knowledge sources, knowledge
// bases). Distinct from FoundryClient because it lives on a different
// service (*.search.windows.net) and uses a different Bearer token scope
// (https://search.azure.com/.default vs Foundry's https://ai.azure.com/.default).
//
// The FoundryClient holds a SearchClient as a sub-resource; resources that
// touch the Search data plane access it via FoundryClient.SearchClient().
// Per-resource search_endpoint values flow into each call rather than being
// baked into the client at construction time, so a single provider
// configuration can manage knowledge bases across multiple Search services.
// ─────────────────────────────────────────────────────────────────────────────

// SearchAPIVersion is the preview api-version pinned for Foundry IQ
// knowledge sources, knowledge bases, and the KB MCP endpoint. Treated as
// opaque: do not parse.
const SearchAPIVersion = "2025-11-01-preview"

// SearchTokenScope is the AAD scope minted for Bearer auth against the
// Azure AI Search data plane.
const SearchTokenScope = "https://search.azure.com/.default"

// SearchClient is a thin authenticated client for the Azure AI Search
// data plane. It mirrors FoundryClient's transport conventions
// (newRequest / checkResponseError / closeBody) but mints tokens against
// SearchTokenScope and accepts the search service endpoint per call.
//
// Auth: only TokenCredential is supported. Search admin-key auth would
// require a separate per-resource secret which we don't expose today;
// the Entra path covers the recommended Foundry IQ setup
// (Project MI → Search Index Data Reader / Contributor on the search
// service).
type SearchClient struct {
	credential azcore.TokenCredential
	httpClient *http.Client
}

func newSearchClient(cred azcore.TokenCredential, httpClient *http.Client) *SearchClient {
	if httpClient == nil {
		httpClient = newHTTPClient()
	}
	return &SearchClient{credential: cred, httpClient: httpClient}
}

// SearchClient returns a sub-client for the Azure AI Search data plane,
// reusing this FoundryClient's TokenCredential. Returns an error when
// the FoundryClient was constructed with API-key auth (NewFoundryClientWithAPIKey)
// — Search data-plane resources require Entra credentials. Lazy and
// goroutine-safe via the readiness mutex; same instance is returned on
// subsequent calls.
func (c *FoundryClient) SearchClient() (*SearchClient, error) {
	if c.authMode == AuthModeAPIKey {
		return nil, errors.New(
			"knowledge source / knowledge base resources require Entra (TokenCredential) " +
				"authentication on the provider, not the Foundry api-key — configure " +
				"tenant_id + client_id + (client_secret | oidc_token), set use_azure_cli = true, " +
				"or rely on the default Azure credential chain",
		)
	}
	c.projectReadyMu.Lock()
	defer c.projectReadyMu.Unlock()
	if c.search == nil {
		c.search = newSearchClient(c.credential, c.httpClient)
	}
	return c.search, nil
}

// SearchEndpoint normalizes a user-supplied search endpoint by stripping a
// trailing slash, so URL builders can concatenate `/path` segments without
// producing `//`.
func SearchEndpoint(endpoint string) string {
	return strings.TrimRight(endpoint, "/")
}

// KnowledgeBaseMCPEndpoint returns the URL agents wire into an mcp tool
// block to consume a Foundry IQ knowledge base. Always uses the preview
// SearchAPIVersion. Hostname comes from the search service endpoint —
// agents on different Foundry projects can target the same KB by URL.
func KnowledgeBaseMCPEndpoint(searchEndpoint, knowledgeBaseName string) string {
	return fmt.Sprintf("%s/knowledgebases/%s/mcp?api-version=%s",
		SearchEndpoint(searchEndpoint), knowledgeBaseName, SearchAPIVersion)
}

// ─────────────────────────────────────────────────────────────────────────────
// Request plumbing
// ─────────────────────────────────────────────────────────────────────────────

// do issues an HTTP request against the Search data plane and decodes a
// JSON response body into result. Pass result=nil for DELETE-style calls.
// 404 is surfaced as a typed *APIError (StatusCode=404) so callers can use
// the existing isNotFound helper from foundry_agent.go.
func (c *SearchClient) do(ctx context.Context, method, target string, body, result any) error {
	httpReq, err := c.newRequest(ctx, method, target, body)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("search API HTTP error (%s %s): %w", method, target, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decoding search response: %w", err)
	}
	return nil
}

func (c *SearchClient) newRequest(ctx context.Context, method, target string, body any) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling search request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating search HTTP request: %w", err)
	}

	tokenOpts := policy.TokenRequestOptions{Scopes: []string{SearchTokenScope}}
	token, err := c.credential.GetToken(ctx, tokenOpts)
	if err != nil {
		return nil, fmt.Errorf("acquiring search token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// PUT against /knowledgebases('{name}') and /knowledgesources('{name}')
	// requires Prefer: return=representation so the response body carries
	// the materialized resource (otherwise Search returns 204 No Content).
	if method == http.MethodPut {
		req.Header.Set("Prefer", "return=representation")
	}
	return req, nil
}
