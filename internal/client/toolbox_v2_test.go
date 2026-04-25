// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCreateToolboxVersion_RoundTripsRequest(t *testing.T) {
	t.Parallel()

	const wantName = "fraud-ops"
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/toolboxes/"+wantName+"/versions") {
			t.Errorf("expected /toolboxes/%s/versions path, got %s", wantName, r.URL.Path)
		}
		if got := r.URL.Query().Get("api-version"); got != ToolboxAPIVersion {
			t.Errorf("expected api-version %q, got %q", ToolboxAPIVersion, got)
		}
		if got := r.Header.Get("Foundry-Features"); !strings.Contains(got, "Toolboxes=V1Preview") {
			t.Errorf("expected Foundry-Features to advertise Toolboxes=V1Preview, got %q", got)
		}

		var payload CreateToolboxVersionRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if payload.Description != "v1" {
			t.Errorf("expected description %q, got %q", "v1", payload.Description)
		}
		if len(payload.Tools) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(payload.Tools))
		}

		body := `{"id":"tbv_123","name":"` + wantName + `","version":"v1","description":"v1","created_at":1700000000,"tools":[{"type":"web_search"}]}`
		return newProbeResponse(http.StatusOK, body), nil
	})

	c := newTestClient(rt)
	resp, err := c.CreateToolboxVersion(context.Background(), wantName, CreateToolboxVersionRequest{
		Description: "v1",
		Tools:       []any{map[string]string{"type": "web_search"}},
	})
	if err != nil {
		t.Fatalf("CreateToolboxVersion: %v", err)
	}
	if resp.Version != "v1" {
		t.Errorf("expected version v1, got %q", resp.Version)
	}
	if resp.ID != "tbv_123" {
		t.Errorf("expected id tbv_123, got %q", resp.ID)
	}
}

func TestGetToolboxVersion_DecodesTools(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/toolboxes/fraud-ops/versions/v1") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body := `{"id":"tbv_123","name":"fraud-ops","version":"v1","tools":[{"type":"mcp","server_label":"x","server_url":"https://x.example.com"}]}`
		return newProbeResponse(http.StatusOK, body), nil
	})

	c := newTestClient(rt)
	resp, err := c.GetToolboxVersion(context.Background(), "fraud-ops", "v1")
	if err != nil {
		t.Fatalf("GetToolboxVersion: %v", err)
	}
	if len(resp.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(resp.Tools))
	}
	tool, ok := resp.Tools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map tool, got %T", resp.Tools[0])
	}
	if tool["type"] != "mcp" {
		t.Errorf("expected mcp tool, got %v", tool["type"])
	}
}

func TestListToolboxVersions_UnwrapsEnvelope(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/toolboxes/fraud-ops/versions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body := `{"object":"list","data":[{"name":"fraud-ops","version":"v1"},{"name":"fraud-ops","version":"v2"}]}`
		return newProbeResponse(http.StatusOK, body), nil
	})

	c := newTestClient(rt)
	versions, err := c.ListToolboxVersions(context.Background(), "fraud-ops")
	if err != nil {
		t.Fatalf("ListToolboxVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
	if versions[1].Version != "v2" {
		t.Errorf("expected second version v2, got %q", versions[1].Version)
	}
}

func TestPromoteToolboxVersion_PatchesDefaultVersion(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/toolboxes/fraud-ops") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var payload UpdateToolboxRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if payload.DefaultVersion != "v2" {
			t.Errorf("expected default_version v2, got %q", payload.DefaultVersion)
		}
		body := `{"id":"tb_abc","name":"fraud-ops","default_version":"v2"}`
		return newProbeResponse(http.StatusOK, body), nil
	})

	c := newTestClient(rt)
	tb, err := c.PromoteToolboxVersion(context.Background(), "fraud-ops", "v2")
	if err != nil {
		t.Fatalf("PromoteToolboxVersion: %v", err)
	}
	if tb.DefaultVersion != "v2" {
		t.Errorf("expected DefaultVersion v2, got %q", tb.DefaultVersion)
	}
}

func TestDeleteToolboxVersion_SurfacesAPIError(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		// Foundry refuses to delete the default version — surface as APIError.
		return &http.Response{
			StatusCode: http.StatusConflict,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"Conflict","message":"Cannot delete default version"}}`)),
			Header:     make(http.Header),
		}, nil
	})

	c := newTestClient(rt)
	err := c.DeleteToolboxVersion(context.Background(), "fraud-ops", "v1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", apiErr.StatusCode)
	}
}

func TestToolboxConsumerEndpoint_BuildsURL(t *testing.T) {
	t.Parallel()

	c := newTestClient(roundTripperFunc(func(_ *http.Request) (*http.Response, error) { return nil, nil }))
	got := c.ToolboxConsumerEndpoint("fraud-ops")
	want := "https://test.example.com/api/projects/test/toolboxes/fraud-ops/mcp?api-version=" + ToolboxAPIVersion
	if got != want {
		t.Errorf("ToolboxConsumerEndpoint:\n got  %s\n want %s", got, want)
	}
}

func TestToolboxVersionedEndpoint_BuildsURL(t *testing.T) {
	t.Parallel()

	c := newTestClient(roundTripperFunc(func(_ *http.Request) (*http.Response, error) { return nil, nil }))
	got := c.ToolboxVersionedEndpoint("fraud-ops", "v2")
	want := "https://test.example.com/api/projects/test/toolboxes/fraud-ops/versions/v2/mcp?api-version=" + ToolboxAPIVersion
	if got != want {
		t.Errorf("ToolboxVersionedEndpoint:\n got  %s\n want %s", got, want)
	}
}
