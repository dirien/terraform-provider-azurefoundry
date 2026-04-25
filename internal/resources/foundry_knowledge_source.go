// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dirien/terraform-provider-azurefoundry/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &FoundryKnowledgeSourceResource{}
	_ resource.ResourceWithImportState = &FoundryKnowledgeSourceResource{}
)

// FoundryKnowledgeSourceResource manages an Azure AI Search Knowledge
// Source (preview). Knowledge sources sit on the Search data plane
// (*.search.windows.net) — distinct from the Foundry data plane —
// and are referenced by knowledge bases via name.
//
// Polymorphic on `kind`. Today the resource implements two variants:
//
//   - kind="azureBlob"   — generates an indexer pipeline against blob storage.
//   - kind="searchIndex" — wraps an existing search index.
//
// Other kinds documented at agentic-knowledge-source-overview
// (indexedOneLake, indexedSharePoint, remoteSharePoint, web) follow the
// same outer envelope and can be added without restructuring this file.
type FoundryKnowledgeSourceResource struct {
	client *client.FoundryClient
}

func NewFoundryKnowledgeSourceResource() resource.Resource {
	return &FoundryKnowledgeSourceResource{}
}

type FoundryKnowledgeSourceResourceModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	SearchEndpoint types.String `tfsdk:"search_endpoint"`
	Kind           types.String `tfsdk:"kind"`
	Description    types.String `tfsdk:"description"`
	ETag           types.String `tfsdk:"etag"`

	AzureBlob   types.Object `tfsdk:"azure_blob"`
	SearchIndex types.Object `tfsdk:"search_index"`
}

var azureBlobAttrTypes = map[string]attr.Type{
	"connection_string":         types.StringType,
	"container_name":            types.StringType,
	"folder_path":               types.StringType,
	"is_adls_gen2":              types.BoolType,
	"ingestion_parameters_json": types.StringType,
}

var searchIndexFieldRefAttrTypes = map[string]attr.Type{
	"name": types.StringType,
}

var searchIndexAttrTypes = map[string]attr.Type{
	"search_index_name":           types.StringType,
	"search_fields":               types.ListType{ElemType: types.ObjectType{AttrTypes: searchIndexFieldRefAttrTypes}},
	"semantic_configuration_name": types.StringType,
	"source_data_fields":          types.ListType{ElemType: types.ObjectType{AttrTypes: searchIndexFieldRefAttrTypes}},
}

func (r *FoundryKnowledgeSourceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_knowledge_source"
}

func (r *FoundryKnowledgeSourceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Azure AI Search **Knowledge Source** (preview, " +
			"`api-version=2025-11-01-preview`).\n\n" +
			"Knowledge sources are the content layer beneath an `azurefoundry_knowledge_base`: " +
			"each source either wraps an existing search index or generates an indexer pipeline " +
			"that pulls into one. Sources, the indexes they produce, and the knowledge base must " +
			"all live on the same Azure AI Search service.\n\n" +
			"### Authentication\n" +
			"This resource requires Entra (TokenCredential) auth on the provider — Foundry " +
			"`api_key` mode is not supported. The provider mints `https://search.azure.com/.default` " +
			"tokens against the `search_endpoint` per call.\n\n" +
			"### Required RBAC\n" +
			"On the target Search service, the calling principal needs " +
			"**Search Service Contributor** plus, for kinds that generate an indexer pipeline " +
			"(`azureBlob`), **Search Index Data Contributor** to load the index. See " +
			"[`agentic-knowledge-source-overview`](https://learn.microsoft.com/azure/search/agentic-knowledge-source-overview) " +
			"for the full role matrix.\n\n" +
			"### Variants\n" +
			"Set the variant block matching `kind`; the other is ignored on the wire.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Synthetic ID `<search_endpoint>|<name>`. Used by `terraform import`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Knowledge source name. Unique within the Search service. Changing this forces replacement.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"search_endpoint": schema.StringAttribute{
				MarkdownDescription: "Azure AI Search service endpoint, e.g. `https://my-search.search.windows.net`. " +
					"Per-resource (not provider-level) so a single provider configuration can manage knowledge " +
					"sources across multiple Search services. Changing this forces replacement.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"kind": schema.StringAttribute{
				MarkdownDescription: "One of `azureBlob` (generates an indexer pipeline against blob storage) or " +
					"`searchIndex` (wraps an existing index). Changing this forces replacement.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(client.KSKindAzureBlob, client.KSKindSearchIndex),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional human-readable description. Surfaced to the LLM during query planning.",
				Optional:            true,
				Computed:            true,
			},
			"etag": schema.StringAttribute{
				MarkdownDescription: "Service-assigned ETag (`@odata.etag`). Updated on every write.",
				Computed:            true,
			},
			"azure_blob": schema.SingleNestedAttribute{
				MarkdownDescription: "Variant params for `kind = \"azureBlob\"`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"connection_string": schema.StringAttribute{
						MarkdownDescription: "Either a key-based connection string " +
							"(`DefaultEndpointsProtocol=https;AccountName=…;AccountKey=…;EndpointSuffix=core.windows.net`) " +
							"or a managed-identity ResourceId form (`ResourceId=/subscriptions/…/storageAccounts/<name>`). " +
							"Treated as sensitive — never logged.",
						Required:  true,
						Sensitive: true,
					},
					"container_name": schema.StringAttribute{
						MarkdownDescription: "Blob container holding the corpus.",
						Required:            true,
					},
					"folder_path": schema.StringAttribute{
						MarkdownDescription: "Optional sub-path within the container.",
						Optional:            true,
					},
					"is_adls_gen2": schema.BoolAttribute{
						MarkdownDescription: "Set `true` when the storage account is ADLS Gen2. Defaults to `false`.",
						Optional:            true,
					},
					"ingestion_parameters_json": schema.StringAttribute{
						MarkdownDescription: "Optional `ingestionParameters` envelope as a JSON string. Use `jsonencode({...})` " +
							"in HCL. Covers chunking, embedding model, schedule, and identity. The Search reference " +
							"documents the full schema; we accept it as-is to track preview shape changes without " +
							"a provider release. Pass `null` or an empty string to omit it.",
						Optional: true,
					},
				},
			},
			"search_index": schema.SingleNestedAttribute{
				MarkdownDescription: "Variant params for `kind = \"searchIndex\"`. Wraps an existing index already populated " +
					"out-of-band (e.g. by `azurerm_search_index` upstream or a separate indexer pipeline).",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"search_index_name": schema.StringAttribute{
						MarkdownDescription: "Name of the existing Search index this source wraps.",
						Required:            true,
					},
					"semantic_configuration_name": schema.StringAttribute{
						MarkdownDescription: "Optional semantic configuration override. Defaults to the index's default semantic config.",
						Optional:            true,
					},
					"search_fields": schema.ListNestedAttribute{
						MarkdownDescription: "Restrict which fields the agentic retrieval engine searches on.",
						Optional:            true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{Required: true, MarkdownDescription: "Index field name."},
							},
						},
					},
					"source_data_fields": schema.ListNestedAttribute{
						MarkdownDescription: "Additional fields returned in the retrieval response payload.",
						Optional:            true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{Required: true, MarkdownDescription: "Index field name."},
							},
						},
					},
				},
			},
		},
	}
}

func (r *FoundryKnowledgeSourceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	apiClient, ok := req.ProviderData.(*client.FoundryClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *client.FoundryClient, got %T.", req.ProviderData),
		)
		return
	}
	r.client = apiClient
}

func (r *FoundryKnowledgeSourceResource) searchClient() (*client.SearchClient, diag.Diagnostics) {
	var diags diag.Diagnostics
	sc, err := r.client.SearchClient()
	if err != nil {
		diags.AddError("Search client unavailable", err.Error())
		return nil, diags
	}
	return sc, diags
}

func (r *FoundryKnowledgeSourceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FoundryKnowledgeSourceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wire, diags := buildKnowledgeSourceWire(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating Foundry knowledge source", map[string]any{
		"name":            plan.Name.ValueString(),
		"kind":            plan.Kind.ValueString(),
		"search_endpoint": plan.SearchEndpoint.ValueString(),
	})

	resp.Diagnostics.Append(r.preflightKSMustNotExist(ctx, sc, plan.SearchEndpoint.ValueString(), plan.Name.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := sc.CreateOrUpdateKnowledgeSource(ctx, plan.SearchEndpoint.ValueString(), wire)
	if err != nil {
		resp.Diagnostics.AddError("Error creating knowledge source", err.Error())
		return
	}

	resp.Diagnostics.Append(applyKnowledgeSourceResponse(ctx, result, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryKnowledgeSourceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FoundryKnowledgeSourceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := sc.GetKnowledgeSource(ctx, state.SearchEndpoint.ValueString(), state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			tflog.Warn(ctx, "Knowledge source no longer exists, removing from state")
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading knowledge source", err.Error())
		return
	}

	resp.Diagnostics.Append(applyKnowledgeSourceResponse(ctx, result, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryKnowledgeSourceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FoundryKnowledgeSourceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wire, diags := buildKnowledgeSourceWire(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := sc.CreateOrUpdateKnowledgeSource(ctx, plan.SearchEndpoint.ValueString(), wire)
	if err != nil {
		resp.Diagnostics.AddError("Error updating knowledge source", err.Error())
		return
	}

	resp.Diagnostics.Append(applyKnowledgeSourceResponse(ctx, result, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryKnowledgeSourceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FoundryKnowledgeSourceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting Foundry knowledge source", map[string]any{
		"name":            state.Name.ValueString(),
		"search_endpoint": state.SearchEndpoint.ValueString(),
	})

	if err := sc.DeleteKnowledgeSource(ctx, state.SearchEndpoint.ValueString(), state.Name.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting knowledge source", err.Error())
	}
}

// ImportState parses a `<search_endpoint>|<name>` synthetic ID. The Search
// service URL is part of the ID because it isn't part of the resource
// path — without it the resource has no idea where to find the KS.
func (r *FoundryKnowledgeSourceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Expected `<search_endpoint>|<name>`, e.g. `https://my-search.search.windows.net|fraud-policies-ks`.",
		)
		return
	}
	searchEndpoint, name := parts[0], parts[1]

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := sc.GetKnowledgeSource(ctx, searchEndpoint, name)
	if err != nil {
		resp.Diagnostics.AddError("Error importing knowledge source", err.Error())
		return
	}

	state := FoundryKnowledgeSourceResourceModel{
		SearchEndpoint: types.StringValue(searchEndpoint),
	}
	resp.Diagnostics.Append(applyKnowledgeSourceResponse(ctx, result, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryKnowledgeSourceResource) preflightKSMustNotExist(ctx context.Context, sc *client.SearchClient, searchEndpoint, name string) diag.Diagnostics {
	var diags diag.Diagnostics
	existing, err := sc.GetKnowledgeSource(ctx, searchEndpoint, name)
	switch {
	case err == nil && existing != nil:
		summary, detail := alreadyExistsError(
			"knowledge source", name,
			"azurefoundry_knowledge_source", "azurefoundry:index:KnowledgeSource",
		)
		diags.AddError(summary, detail)
	case err != nil && !isNotFound(err):
		diags.AddError("Pre-flight existence check failed", err.Error())
	}
	return diags
}

// ─────────────────────────────────────────────────────────────────────────────
// Mapping helpers
// ─────────────────────────────────────────────────────────────────────────────

func buildKnowledgeSourceWire(ctx context.Context, m FoundryKnowledgeSourceResourceModel) (client.KnowledgeSourceWire, diag.Diagnostics) {
	var diags diag.Diagnostics
	wire := client.KnowledgeSourceWire{
		Name:        m.Name.ValueString(),
		Kind:        m.Kind.ValueString(),
		Description: m.Description.ValueString(),
	}

	switch wire.Kind {
	case client.KSKindAzureBlob:
		params, d := buildAzureBlobKSParams(ctx, m.AzureBlob)
		diags.Append(d...)
		wire.AzureBlobParameters = params
		if !m.SearchIndex.IsNull() && !m.SearchIndex.IsUnknown() {
			diags.AddWarning(
				"search_index ignored",
				"`search_index` is set but `kind = \"azureBlob\"` — the `search_index` block will be ignored on the wire.",
			)
		}
	case client.KSKindSearchIndex:
		params, d := buildSearchIndexKSParams(ctx, m.SearchIndex)
		diags.Append(d...)
		wire.SearchIndexParameters = params
		if !m.AzureBlob.IsNull() && !m.AzureBlob.IsUnknown() {
			diags.AddWarning(
				"azure_blob ignored",
				"`azure_blob` is set but `kind = \"searchIndex\"` — the `azure_blob` block will be ignored on the wire.",
			)
		}
	default:
		diags.AddError("Unsupported knowledge source kind", fmt.Sprintf("kind %q not supported by this provider", wire.Kind))
	}
	return wire, diags
}

func buildAzureBlobKSParams(_ context.Context, obj types.Object) (*client.AzureBlobKSParameters, diag.Diagnostics) {
	var diags diag.Diagnostics
	if obj.IsNull() || obj.IsUnknown() {
		diags.AddError("Missing azure_blob block", "`kind = \"azureBlob\"` requires an `azure_blob` block.")
		return nil, diags
	}
	attrs := obj.Attributes()
	params := &client.AzureBlobKSParameters{
		ConnectionString: stringAttr(attrs, "connection_string"),
		ContainerName:    stringAttr(attrs, "container_name"),
		FolderPath:       stringAttr(attrs, "folder_path"),
	}
	if v, ok := attrs["is_adls_gen2"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
		params.IsADLSGen2 = v.ValueBool()
	}
	if raw := stringAttr(attrs, "ingestion_parameters_json"); raw != "" {
		var ingest map[string]any
		if err := json.Unmarshal([]byte(raw), &ingest); err != nil {
			diags.AddError("Invalid ingestion_parameters_json", err.Error())
			return nil, diags
		}
		params.IngestionParameters = ingest
	}
	return params, diags
}

func buildSearchIndexKSParams(ctx context.Context, obj types.Object) (*client.SearchIndexKSParameters, diag.Diagnostics) {
	var diags diag.Diagnostics
	if obj.IsNull() || obj.IsUnknown() {
		diags.AddError("Missing search_index block", "`kind = \"searchIndex\"` requires a `search_index` block.")
		return nil, diags
	}
	attrs := obj.Attributes()
	params := &client.SearchIndexKSParameters{
		SearchIndexName:           stringAttr(attrs, "search_index_name"),
		SemanticConfigurationName: stringAttr(attrs, "semantic_configuration_name"),
	}
	if v, ok := attrs["search_fields"].(types.List); ok {
		params.SearchFields = extractFieldRefs(ctx, v)
	}
	if v, ok := attrs["source_data_fields"].(types.List); ok {
		params.SourceDataFields = extractFieldRefs(ctx, v)
	}
	return params, diags
}

func extractFieldRefs(_ context.Context, l types.List) []client.SearchIndexFieldRef {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]client.SearchIndexFieldRef, 0, len(l.Elements()))
	for _, elem := range l.Elements() {
		obj, ok := elem.(types.Object)
		if !ok {
			continue
		}
		out = append(out, client.SearchIndexFieldRef{Name: stringAttr(obj.Attributes(), "name")})
	}
	return out
}

func applyKnowledgeSourceResponse(_ context.Context, r *client.KnowledgeSourceWire, m *FoundryKnowledgeSourceResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(m.SearchEndpoint.ValueString() + "|" + r.Name)
	m.Name = types.StringValue(r.Name)
	m.Kind = types.StringValue(r.Kind)
	m.Description = types.StringValue(r.Description)
	m.ETag = types.StringValue(r.ETag)

	switch r.Kind {
	case client.KSKindAzureBlob:
		obj, d := wireAzureBlobToObject(r.AzureBlobParameters, m.AzureBlob)
		diags.Append(d...)
		m.AzureBlob = obj
		m.SearchIndex = types.ObjectNull(searchIndexAttrTypes)
	case client.KSKindSearchIndex:
		obj, d := wireSearchIndexToObject(r.SearchIndexParameters)
		diags.Append(d...)
		m.SearchIndex = obj
		m.AzureBlob = types.ObjectNull(azureBlobAttrTypes)
	default:
		m.AzureBlob = types.ObjectNull(azureBlobAttrTypes)
		m.SearchIndex = types.ObjectNull(searchIndexAttrTypes)
	}
	return diags
}

// wireAzureBlobToObject preserves the user-supplied connection_string from
// state — Search redacts it from GET responses (returns the placeholder
// "<redacted>"), and reading that back would constantly produce drift.
// folder_path / is_adls_gen2 / ingestion_parameters_json round-trip
// through the wire fine.
func wireAzureBlobToObject(p *client.AzureBlobKSParameters, prior types.Object) (types.Object, diag.Diagnostics) {
	if p == nil {
		return types.ObjectNull(azureBlobAttrTypes), nil
	}
	connectionString := p.ConnectionString
	if priorAttrs := prior.Attributes(); priorAttrs != nil {
		if existing := stringAttr(priorAttrs, "connection_string"); existing != "" {
			connectionString = existing
		}
	}
	ingestionJSON := ""
	if p.IngestionParameters != nil {
		if buf, err := json.Marshal(p.IngestionParameters); err == nil {
			ingestionJSON = string(buf)
		}
	}
	return types.ObjectValue(azureBlobAttrTypes, map[string]attr.Value{
		"connection_string":         types.StringValue(connectionString),
		"container_name":            types.StringValue(p.ContainerName),
		"folder_path":               types.StringValue(p.FolderPath),
		"is_adls_gen2":              types.BoolValue(p.IsADLSGen2),
		"ingestion_parameters_json": types.StringValue(ingestionJSON),
	})
}

func wireSearchIndexToObject(p *client.SearchIndexKSParameters) (types.Object, diag.Diagnostics) {
	if p == nil {
		return types.ObjectNull(searchIndexAttrTypes), nil
	}
	var diags diag.Diagnostics
	searchFields, d := fieldRefsToList(p.SearchFields)
	diags.Append(d...)
	sourceFields, d := fieldRefsToList(p.SourceDataFields)
	diags.Append(d...)
	obj, d := types.ObjectValue(searchIndexAttrTypes, map[string]attr.Value{
		"search_index_name":           types.StringValue(p.SearchIndexName),
		"semantic_configuration_name": types.StringValue(p.SemanticConfigurationName),
		"search_fields":               searchFields,
		"source_data_fields":          sourceFields,
	})
	diags.Append(d...)
	return obj, diags
}

func fieldRefsToList(refs []client.SearchIndexFieldRef) (types.List, diag.Diagnostics) {
	objs := make([]attr.Value, 0, len(refs))
	var diags diag.Diagnostics
	for _, ref := range refs {
		obj, d := types.ObjectValue(searchIndexFieldRefAttrTypes, map[string]attr.Value{
			"name": types.StringValue(ref.Name),
		})
		diags.Append(d...)
		objs = append(objs, obj)
	}
	list, d := types.ListValue(types.ObjectType{AttrTypes: searchIndexFieldRefAttrTypes}, objs)
	diags.Append(d...)
	return list, diags
}
