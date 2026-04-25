// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// ─────────────────────────────────────────────────────────────────────────────
// Knowledge Base — Azure AI Search data plane (preview).
//
// REST surface, api-version=2025-11-01-preview:
//
//	PUT    {search}/knowledgebases('{name}')?api-version=…
//	GET    {search}/knowledgebases('{name}')?api-version=…
//	DELETE {search}/knowledgebases('{name}')?api-version=…
//	GET    {search}/knowledgebases?api-version=…             (list)
//
// MCP consumer endpoint (read-only, used at agent inference time):
//
//	{search}/knowledgebases/{name}/mcp?api-version=…
//
// See KnowledgeBaseMCPEndpoint() in search_client.go for the helper that
// builds it; resources expose it as a computed `mcp_endpoint` attribute.
// ─────────────────────────────────────────────────────────────────────────────

// KB output mode and reasoning effort enums.
const (
	KBOutputModeExtractiveData  = "extractiveData"
	KBOutputModeAnswerSynthesis = "answerSynthesis"
	KBReasoningEffortMinimal    = "minimal"
	KBReasoningEffortLow        = "low"
	KBReasoningEffortMedium     = "medium"
	KBModelKindAzureOpenAI      = "azureOpenAI"
)

// KnowledgeBaseWire is the on-the-wire representation. The Search API
// always echoes back name + knowledgeSources + models (when set); the
// optional fields are only present when the caller set them.
type KnowledgeBaseWire struct {
	Name                     string                       `json:"name"`
	Description              string                       `json:"description,omitempty"`
	ETag                     string                       `json:"@odata.etag,omitempty"`
	RetrievalInstructions    string                       `json:"retrievalInstructions,omitempty"`
	AnswerInstructions       string                       `json:"answerInstructions,omitempty"`
	OutputMode               string                       `json:"outputMode,omitempty"`
	KnowledgeSources         []KnowledgeSourceRef         `json:"knowledgeSources"`
	Models                   []KnowledgeBaseModel         `json:"models,omitempty"`
	RetrievalReasoningEffort *KnowledgeRetrievalReasoning `json:"retrievalReasoningEffort,omitempty"`
}

// KnowledgeSourceRef is the {"name": "..."} reference used inside a KB's
// knowledgeSources[] array. The KS must already exist on the same Search
// service.
type KnowledgeSourceRef struct {
	Name string `json:"name"`
}

// KnowledgeBaseModel is one entry in models[]. Today only kind="azureOpenAI"
// is supported by Search; the discriminator stays explicit so adding new
// kinds later is one struct + one map entry.
type KnowledgeBaseModel struct {
	Kind                  string                    `json:"kind"`
	AzureOpenAIParameters *AzureOpenAIVectorizerCfg `json:"azureOpenAIParameters,omitempty"`
}

// KnowledgeRetrievalReasoning is the {"kind": "low|medium|minimal"} envelope
// — the API uses a polymorphic discriminator with no other fields per kind
// today, so a single struct covers all three variants.
type KnowledgeRetrievalReasoning struct {
	Kind string `json:"kind"`
}

// ─────────────────────────────────────────────────────────────────────────────
// CRUD
// ─────────────────────────────────────────────────────────────────────────────

func knowledgeBaseURL(searchEndpoint, name string) string {
	return fmt.Sprintf("%s/knowledgebases('%s')?api-version=%s",
		SearchEndpoint(searchEndpoint), url.PathEscape(name), SearchAPIVersion)
}

func (c *SearchClient) CreateOrUpdateKnowledgeBase(ctx context.Context, searchEndpoint string, kb KnowledgeBaseWire) (*KnowledgeBaseWire, error) {
	target := knowledgeBaseURL(searchEndpoint, kb.Name)
	var result KnowledgeBaseWire
	if err := c.do(ctx, http.MethodPut, target, kb, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *SearchClient) GetKnowledgeBase(ctx context.Context, searchEndpoint, name string) (*KnowledgeBaseWire, error) {
	target := knowledgeBaseURL(searchEndpoint, name)
	var result KnowledgeBaseWire
	if err := c.do(ctx, http.MethodGet, target, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *SearchClient) DeleteKnowledgeBase(ctx context.Context, searchEndpoint, name string) error {
	target := knowledgeBaseURL(searchEndpoint, name)
	return c.do(ctx, http.MethodDelete, target, nil, nil)
}
