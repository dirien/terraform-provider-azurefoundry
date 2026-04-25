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
// Vector store — model types (shared between v1 and v2)
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

// VectorStoreFileCounts is the subset of Foundry's file_counts envelope the
// provider exposes. Foundry also returns in_progress and canceled counts;
// they're discarded by the JSON decoder because no resource maps them
// through to state.
type VectorStoreFileCounts struct {
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
	Total     int64 `json:"total"`
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
// Vector store CRUD (classic /vector_stores?api-version=APIVersion)
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
	return c.deleteVectorStore(ctx, url)
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

// ─────────────────────────────────────────────────────────────────────────────
// Vector store CRUD (v2 /vector_stores?api-version=v1)
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) CreateVectorStoreV2(ctx context.Context, req CreateVectorStoreRequest) (*VectorStoreResponse, error) {
	url := c.ProjectEndpoint + "/vector_stores?api-version=v1"
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
	return c.deleteVectorStore(ctx, url)
}

// ─────────────────────────────────────────────────────────────────────────────
// Vector store helpers
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) doVectorStoreRequest(ctx context.Context, method, url string, body any) (*VectorStoreResponse, error) {
	httpReq, err := c.newRequest(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vector store API HTTP error (%s %s): %w", method, url, err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result VectorStoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding vector store response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) deleteVectorStore(ctx context.Context, url string) (*DeleteVectorStoreResponse, error) {
	httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete vector store HTTP error: %w", err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteVectorStoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete vector store response: %w", err)
	}
	return &result, nil
}
