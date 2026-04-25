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
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// stubTokenCredential mints a fixed token so SearchClient HTTP requests
// don't need a real Entra back-end during unit tests.
type stubTokenCredential struct{ token string }

func (s stubTokenCredential) GetToken(_ context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: s.token, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func newSearchTestClient(rt http.RoundTripper) *SearchClient {
	return newSearchClient(stubTokenCredential{token: "test-token"}, &http.Client{Transport: rt})
}

func TestSearchClient_RequiresEntraAuth(t *testing.T) {
	t.Parallel()

	c := NewFoundryClientWithAPIKey("https://x.example.com", "key")
	if _, err := c.SearchClient(); err == nil {
		t.Fatal("expected error for api-key auth, got nil")
	}
}

func TestKnowledgeSource_RoundTripsAzureBlobBody(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/knowledgesources('fraud-policies-ks')") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("api-version"); got != SearchAPIVersion {
			t.Errorf("expected api-version %q, got %q", SearchAPIVersion, got)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("expected Bearer auth, got %q", got)
		}
		if got := r.Header.Get("Prefer"); got != "return=representation" {
			t.Errorf("expected Prefer=return=representation on PUT, got %q", got)
		}

		var payload KnowledgeSourceWire
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if payload.Kind != KSKindAzureBlob {
			t.Errorf("expected kind=azureBlob, got %q", payload.Kind)
		}
		if payload.AzureBlobParameters == nil || payload.AzureBlobParameters.ContainerName != "fraud-policies" {
			t.Errorf("unexpected azureBlob params: %+v", payload.AzureBlobParameters)
		}
		if payload.SearchIndexParameters != nil {
			t.Error("searchIndexParameters must be omitted for kind=azureBlob")
		}

		body := `{"name":"fraud-policies-ks","kind":"azureBlob","@odata.etag":"0x1","azureBlobParameters":{"connectionString":"x","containerName":"fraud-policies"}}`
		return newProbeResponse(http.StatusOK, body), nil
	})

	c := newSearchTestClient(rt)
	resp, err := c.CreateOrUpdateKnowledgeSource(context.Background(), "https://test.search.windows.net", KnowledgeSourceWire{
		Name: "fraud-policies-ks",
		Kind: KSKindAzureBlob,
		AzureBlobParameters: &AzureBlobKSParameters{
			ConnectionString: "x",
			ContainerName:    "fraud-policies",
		},
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateKnowledgeSource: %v", err)
	}
	if resp.ETag != "0x1" {
		t.Errorf("expected etag round-tripped, got %q", resp.ETag)
	}
}

func TestKnowledgeSource_DeleteSurfacesNotFoundAsAPIError(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return newProbeResponse(http.StatusNotFound, `{"error":{"code":"NotFound","message":"gone"}}`), nil
	})

	c := newSearchTestClient(rt)
	err := c.DeleteKnowledgeSource(context.Background(), "https://test.search.windows.net", "missing-ks")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("expected APIError 404, got %v (%T)", err, err)
	}
}

func TestKnowledgeBase_CreateBuildsCanonicalBody(t *testing.T) {
	t.Parallel()

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/knowledgebases('fraud-policy-kb')") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var payload KnowledgeBaseWire
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if len(payload.KnowledgeSources) != 1 || payload.KnowledgeSources[0].Name != "fraud-policies-ks" {
			t.Errorf("unexpected knowledgeSources: %+v", payload.KnowledgeSources)
		}
		if len(payload.Models) != 1 || payload.Models[0].Kind != KBModelKindAzureOpenAI {
			t.Errorf("unexpected models: %+v", payload.Models)
		}
		if payload.RetrievalReasoningEffort == nil || payload.RetrievalReasoningEffort.Kind != KBReasoningEffortLow {
			t.Errorf("expected retrievalReasoningEffort.kind=low, got %+v", payload.RetrievalReasoningEffort)
		}

		body := `{"name":"fraud-policy-kb","knowledgeSources":[{"name":"fraud-policies-ks"}]}`
		return newProbeResponse(http.StatusOK, body), nil
	})

	c := newSearchTestClient(rt)
	_, err := c.CreateOrUpdateKnowledgeBase(context.Background(), "https://test.search.windows.net", KnowledgeBaseWire{
		Name:             "fraud-policy-kb",
		KnowledgeSources: []KnowledgeSourceRef{{Name: "fraud-policies-ks"}},
		Models: []KnowledgeBaseModel{
			{
				Kind: KBModelKindAzureOpenAI,
				AzureOpenAIParameters: &AzureOpenAIVectorizerCfg{
					ResourceURI:  "https://x.openai.azure.com",
					DeploymentID: "gpt-4o-mini",
					ModelName:    "gpt-4o-mini",
				},
			},
		},
		RetrievalReasoningEffort: &KnowledgeRetrievalReasoning{Kind: KBReasoningEffortLow},
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateKnowledgeBase: %v", err)
	}
}

func TestKnowledgeBaseMCPEndpoint_BuildsURL(t *testing.T) {
	t.Parallel()

	got := KnowledgeBaseMCPEndpoint("https://x.search.windows.net/", "fraud-policy-kb")
	want := "https://x.search.windows.net/knowledgebases/fraud-policy-kb/mcp?api-version=" + SearchAPIVersion
	if got != want {
		t.Errorf("KnowledgeBaseMCPEndpoint:\n got  %s\n want %s", got, want)
	}
}
