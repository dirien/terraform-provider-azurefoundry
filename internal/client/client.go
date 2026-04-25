// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

// Package client is a thin HTTP client for the Azure AI Foundry data-plane.
// It is split per resource family — see agent.go, agent_v2.go, file.go,
// vector_store.go, and memory_store_v2.go for the typed CRUD surfaces.
// This file owns transport: client construction, auth, request building,
// readiness probing, and error mapping.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// APIVersion is the stable Foundry data-plane api-version pinned for v1
// surfaces (assistants, files, vector_stores). v2 surfaces use ?api-version=v1
// and the memory-store preview pins its own version (see memory_store_v2.go).
const APIVersion = "2025-05-01"

// AuthMode selects between API-key and azidentity TokenCredential auth.
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

// NewFoundryClientWithCredential builds a client that uses the given
// azidentity TokenCredential to mint Bearer tokens for the AAD scope
// "https://ai.azure.com/.default".
func NewFoundryClientWithCredential(projectEndpoint string, credential azcore.TokenCredential) *FoundryClient {
	return &FoundryClient{
		ProjectEndpoint: projectEndpoint,
		authMode:        AuthModeAzureCredential,
		credential:      credential,
		httpClient:      newHTTPClient(),
	}
}

// NewFoundryClientWithAPIKey builds a client that authenticates with the
// "api-key" header (Foundry's static project key).
func NewFoundryClientWithAPIKey(projectEndpoint, apiKey string) *FoundryClient {
	return &FoundryClient{
		ProjectEndpoint: projectEndpoint,
		authMode:        AuthModeAPIKey,
		apiKey:          apiKey,
		httpClient:      newHTTPClient(),
	}
}

// WaitForProjectReady polls the Foundry data-plane endpoint until a cheap
// GET on /files returns 2xx. This covers two intertwined startup races:
//
//  1. ARM finishes creating the project resource before the data-plane
//     project routing is ready (Foundry returns HTTP 404
//     "Project not found" until it is).
//  2. RBAC role assignments take 10–30 minutes to propagate to Foundry's
//     access-check cache. While they propagate, the data plane returns
//     401/403 even though the principal does have the role server-side.
//
// /files on the v1 surface (api-version=APIVersion) is intentional: it's
// universally available on every Foundry project regardless of whether the
// underlying account has an Agents capability host. Probing /agents on the
// v2 surface returns a permanent 404 ("Project not found") for prompt-only
// projects without an AccountCapabilityHost of kind Agents — which would
// hang Create on those projects until the timeout fires even though the
// data plane is fully up.
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

	url := c.ProjectEndpoint + "/files?api-version=" + APIVersion
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
			closeBody(resp)
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
// Request plumbing
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) newRequest(ctx context.Context, method, url string, body any) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
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

// ─────────────────────────────────────────────────────────────────────────────
// Error type & response helpers
// ─────────────────────────────────────────────────────────────────────────────

// APIError is returned for any non-2xx Foundry response. The raw body is
// preserved so callers can pattern-match on error codes (e.g. 409 conflict,
// 424 session_not_ready) without re-parsing the wire JSON.
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

// closeBody drains and closes an HTTP response body. Drain is required for
// keep-alive connection reuse; the close error is intentionally swallowed
// because the response has already been consumed by the caller and there
// is nothing actionable to report.
func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
