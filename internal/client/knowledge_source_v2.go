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
// Knowledge Source — Azure AI Search data plane (preview).
//
// REST surface, api-version=2025-11-01-preview:
//
//	PUT    {search}/knowledgesources('{name}')?api-version=…
//	GET    {search}/knowledgesources('{name}')?api-version=…
//	DELETE {search}/knowledgesources('{name}')?api-version=…
//	GET    {search}/knowledgesources?api-version=…           (list)
//
// Polymorphic on `kind`. This package exposes the two kinds called out
// in issue #4 — searchIndex (wraps an existing index) and azureBlob
// (auto-generates an indexer pipeline). The other kinds documented at
// learn.microsoft.com/azure/search/agentic-knowledge-source-overview
// (indexedOneLake, indexedSharePoint, remoteSharePoint, web) follow the
// same outer envelope and can be added by extending KnowledgeSourceWire.
// ─────────────────────────────────────────────────────────────────────────────

// KS kind discriminators on the wire. Constants keep the typo surface tight.
const (
	KSKindAzureBlob   = "azureBlob"
	KSKindSearchIndex = "searchIndex"
)

// KnowledgeSourceWire is the on-the-wire representation of a knowledge
// source. The variant-specific parameters live in optional pointer
// fields; exactly one matches the kind. Unspecified pointer fields are
// omitted from the JSON body so Search doesn't reject a mixed envelope.
type KnowledgeSourceWire struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	ETag        string `json:"@odata.etag,omitempty"`

	AzureBlobParameters   *AzureBlobKSParameters   `json:"azureBlobParameters,omitempty"`
	SearchIndexParameters *SearchIndexKSParameters `json:"searchIndexParameters,omitempty"`
}

// AzureBlobKSParameters are the variant params for kind="azureBlob".
// connectionString accepts either a key-based connection string (DefaultEndpointsProtocol=…)
// or a managed-identity ResourceId form ("ResourceId=/subscriptions/…/storageAccounts/<name>"),
// per the Search docs. ingestionParameters is left as a generic map for the
// first cut — its inner schema (chunking, embedding model, schedule) is
// large and stable enough that JSON pass-through is the lower-risk path
// than re-typing every field today.
type AzureBlobKSParameters struct {
	ConnectionString    string         `json:"connectionString"`
	ContainerName       string         `json:"containerName"`
	FolderPath          string         `json:"folderPath,omitempty"`
	IsADLSGen2          bool           `json:"isADLSGen2,omitempty"`
	IngestionParameters map[string]any `json:"ingestionParameters,omitempty"`
}

// SearchIndexKSParameters are the variant params for kind="searchIndex".
type SearchIndexKSParameters struct {
	SearchIndexName           string                    `json:"searchIndexName"`
	SearchFields              []SearchIndexFieldRef     `json:"searchFields,omitempty"`
	SemanticConfigurationName string                    `json:"semanticConfigurationName,omitempty"`
	SourceDataFields          []SearchIndexFieldRef     `json:"sourceDataFields,omitempty"`
	_                         struct{}                  `json:"-"` // padding so struct stays open to additions
	Embedding                 *AzureOpenAIVectorizerCfg `json:"-"` // reserved for future extension
}

// SearchIndexFieldRef is the {"name": "..."} envelope used in
// searchFields[] and sourceDataFields[].
type SearchIndexFieldRef struct {
	Name string `json:"name"`
}

// AzureOpenAIVectorizerCfg is shared between knowledge source ingestion
// (embedding model) and knowledge base models[] entries. Either apiKey
// OR authIdentity should be set, not both.
type AzureOpenAIVectorizerCfg struct {
	ResourceURI  string                 `json:"resourceUri"`
	DeploymentID string                 `json:"deploymentId"`
	ModelName    string                 `json:"modelName,omitempty"`
	APIKey       string                 `json:"apiKey,omitempty"`
	AuthIdentity *SearchIndexerIdentity `json:"authIdentity,omitempty"`
}

// SearchIndexerIdentity carries the Search "@odata.type" identity
// envelope used by encryption keys, indexer data sources, and the KS
// embedding model auth. ODataType is one of:
//   - "#Microsoft.Azure.Search.DataNoneIdentity" (clear)
//   - "#Microsoft.Azure.Search.DataUserAssignedIdentity" (with userAssignedIdentity)
type SearchIndexerIdentity struct {
	ODataType            string `json:"@odata.type"`
	UserAssignedIdentity string `json:"userAssignedIdentity,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// CRUD
// ─────────────────────────────────────────────────────────────────────────────

func knowledgeSourceURL(searchEndpoint, name string) string {
	return fmt.Sprintf("%s/knowledgesources('%s')?api-version=%s",
		SearchEndpoint(searchEndpoint), url.PathEscape(name), SearchAPIVersion)
}

// CreateOrUpdateKnowledgeSource is the PUT variant — Search treats it as
// upsert. Returns the materialized resource via Prefer:
// return=representation (set by SearchClient.newRequest).
func (c *SearchClient) CreateOrUpdateKnowledgeSource(ctx context.Context, searchEndpoint string, ks KnowledgeSourceWire) (*KnowledgeSourceWire, error) {
	target := knowledgeSourceURL(searchEndpoint, ks.Name)
	var result KnowledgeSourceWire
	if err := c.do(ctx, http.MethodPut, target, ks, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *SearchClient) GetKnowledgeSource(ctx context.Context, searchEndpoint, name string) (*KnowledgeSourceWire, error) {
	target := knowledgeSourceURL(searchEndpoint, name)
	var result KnowledgeSourceWire
	if err := c.do(ctx, http.MethodGet, target, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *SearchClient) DeleteKnowledgeSource(ctx context.Context, searchEndpoint, name string) error {
	target := knowledgeSourceURL(searchEndpoint, name)
	return c.do(ctx, http.MethodDelete, target, nil, nil)
}
