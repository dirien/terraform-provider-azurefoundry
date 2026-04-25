// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestCreateOrUpdateProjectIndex_RoundTripsAzureSearchBody(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		// PATCH, not PUT — `create_or_update` in the SDK is RFC 7396 merge-
		// patch transport. v0.8.2 used PUT and got 404 from the live
		// service (issue #12); the URL template stayed correct.
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/indexes/fraud-policies-index/versions/1") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("api-version"); got != ProjectIndexAPIVersion {
			t.Errorf("expected api-version %q, got %q", ProjectIndexAPIVersion, got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/merge-patch+json" {
			t.Errorf("expected content-type merge-patch+json, got %q", got)
		}

		var payload ProjectIndex
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if payload.Type != ProjectIndexTypeAzureSearch {
			t.Errorf("expected type=AzureSearch, got %q", payload.Type)
		}
		if payload.ConnectionName != "search-conn" {
			t.Errorf("expected connection_name=search-conn, got %q", payload.ConnectionName)
		}
		if payload.IndexName != "fraud-policies-ks-index" {
			t.Errorf("expected index_name=fraud-policies-ks-index, got %q", payload.IndexName)
		}
		if payload.Version != "1" {
			t.Errorf("expected version=1, got %q", payload.Version)
		}

		body := `{"name":"fraud-policies-index","version":"1","type":"AzureSearch","id":"idx_abc","connectionName":"search-conn","indexName":"fraud-policies-ks-index"}`
		return newProbeResponse(http.StatusOK, body), nil
	})

	c := newTestClient(rt)
	resp, err := c.CreateOrUpdateProjectIndex(context.Background(), ProjectIndex{
		Name:           "fraud-policies-index",
		Type:           ProjectIndexTypeAzureSearch,
		ConnectionName: "search-conn",
		IndexName:      "fraud-policies-ks-index",
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateProjectIndex: %v", err)
	}
	if resp.ID != "idx_abc" {
		t.Errorf("expected ID round-tripped, got %q", resp.ID)
	}
	if resp.Version != "1" {
		t.Errorf("expected version 1, got %q", resp.Version)
	}
}

func TestProjectIndex_VersionDefaultsToOne(t *testing.T) {
	t.Parallel()

	c := newTestClient(roundTripperFunc(func(_ *http.Request) (*http.Response, error) { return nil, nil }))

	got := c.projectIndexURL("x", "")
	want := "https://test.example.com/api/projects/test/indexes/x/versions/?api-version=" + ProjectIndexAPIVersion
	if got != want {
		t.Errorf("empty version URL builder still encodes correctly:\n got  %s\n want %s", got, want)
	}

	// The service-facing CRUD methods substitute the default themselves —
	// verified end-to-end by other tests in this file. The URL builder
	// stays version-faithful so a caller that explicitly passes "" sees
	// the truncated path and gets a service error rather than silently
	// hitting "/versions/1".
}

func TestGetProjectIndex_404SurfacesAsAPIError(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return newProbeResponse(http.StatusNotFound, `{"error":{"code":"NotFound","message":"index missing"}}`), nil
	})

	c := newTestClient(rt)
	_, err := c.GetProjectIndex(context.Background(), "missing-index", "1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("expected APIError 404, got %v (%T)", err, err)
	}
}

func TestDeleteProjectIndex_NoBodyOnSuccess(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/indexes/x/versions/1") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		return newProbeResponse(http.StatusNoContent, ""), nil
	})

	c := newTestClient(rt)
	if err := c.DeleteProjectIndex(context.Background(), "x", "1"); err != nil {
		t.Fatalf("DeleteProjectIndex: %v", err)
	}
}

// TestCreateOrUpdateProjectIndex_BodyIsFlatCamelCase pins the wire shape
// against issue #14. Foundry's PATCH handler validates the flat envelope:
// it expects connectionName / indexName / fieldMapping at the top level
// alongside type, NOT nested under an azureSearch sub-object NOR using
// snake_case keys. v0.8.2 / v0.8.3 used snake_case (connection_name /
// index_name / field_mapping) and got HTTP 400 "ConnectionName field is
// required" because Foundry didn't recognize the keys.
//
// The test asserts the raw bytes — not just a struct round-trip — so a
// future tag drift back to snake_case fails loudly.
func TestCreateOrUpdateProjectIndex_BodyIsFlatCamelCase(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		buf := make([]byte, r.ContentLength)
		if _, err := r.Body.Read(buf); err != nil && err.Error() != "EOF" {
			t.Fatalf("reading body: %v", err)
		}
		raw := string(buf)

		// Required keys live at the TOP level alongside type, in camelCase.
		mustContain := []string{
			`"type":"AzureSearch"`,
			`"connectionName":"search-conn"`,
			`"indexName":"fraud-policies-ks-index"`,
		}
		for _, want := range mustContain {
			if !strings.Contains(raw, want) {
				t.Errorf("expected body to contain %q, got %s", want, raw)
			}
		}

		// Specific anti-patterns from the v0.8.2 / v0.8.3 regression: no
		// snake_case keys, no azureSearch sub-envelope.
		mustNotContain := []string{
			`"connection_name"`,
			`"index_name"`,
			`"field_mapping"`,
			`"azureSearch"`,
			`"azure_search"`,
		}
		for _, no := range mustNotContain {
			if strings.Contains(raw, no) {
				t.Errorf("body must not contain %q, got %s", no, raw)
			}
		}

		return newProbeResponse(http.StatusOK, `{"name":"x","version":"1","type":"AzureSearch","connectionName":"search-conn","indexName":"fraud-policies-ks-index"}`), nil
	})

	c := newTestClient(rt)
	if _, err := c.CreateOrUpdateProjectIndex(context.Background(), ProjectIndex{
		Name:           "x",
		Type:           ProjectIndexTypeAzureSearch,
		ConnectionName: "search-conn",
		IndexName:      "fraud-policies-ks-index",
	}); err != nil {
		t.Fatalf("CreateOrUpdateProjectIndex: %v", err)
	}
}

func TestProjectIndex_FieldMappingRoundTrips(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var payload ProjectIndex
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if payload.FieldMapping == nil {
			t.Fatal("expected field_mapping to be set")
		}
		if len(payload.FieldMapping.ContentFields) != 2 || payload.FieldMapping.ContentFields[0] != "title" {
			t.Errorf("unexpected content_fields: %+v", payload.FieldMapping.ContentFields)
		}
		if payload.FieldMapping.URLField != "source_url" {
			t.Errorf("unexpected url_field: %q", payload.FieldMapping.URLField)
		}

		body := `{"name":"x","version":"1","type":"AzureSearch","connectionName":"c","indexName":"i","fieldMapping":{"contentFields":["title","body"],"urlField":"source_url"}}`
		return newProbeResponse(http.StatusOK, body), nil
	})

	c := newTestClient(rt)
	resp, err := c.CreateOrUpdateProjectIndex(context.Background(), ProjectIndex{
		Name:           "x",
		Type:           ProjectIndexTypeAzureSearch,
		ConnectionName: "c",
		IndexName:      "i",
		FieldMapping: &FieldMapping{
			ContentFields: []string{"title", "body"},
			URLField:      "source_url",
		},
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateProjectIndex: %v", err)
	}
	if resp.FieldMapping == nil || resp.FieldMapping.URLField != "source_url" {
		t.Errorf("FieldMapping didn't round-trip: %+v", resp.FieldMapping)
	}
}
