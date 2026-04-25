// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package resources

import (
	"context"
	"fmt"

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
	_ resource.Resource                = &FoundryProjectIndexResource{}
	_ resource.ResourceWithImportState = &FoundryProjectIndexResource{}
)

// FoundryProjectIndexResource manages a Foundry project-level Index
// registration. These are the entries the Foundry portal's
// **Foundry IQ → Indexes** tab lists — separate from the underlying
// Azure AI Search indexes themselves (which `azurerm_search_index` /
// `azurerm_cognitive_account_project_connection` already cover).
//
// Today only `kind = "AzureSearch"` is implemented; the SDK leaves room
// for future variants and the dispatch is shaped to take them.
type FoundryProjectIndexResource struct {
	client *client.FoundryClient
}

func NewFoundryProjectIndexResource() resource.Resource {
	return &FoundryProjectIndexResource{}
}

type FoundryProjectIndexResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Kind        types.String `tfsdk:"kind"`
	Description types.String `tfsdk:"description"`
	Tags        types.Map    `tfsdk:"tags"`
	AzureSearch types.Object `tfsdk:"azure_search"`
}

var fieldMappingAttrTypes = map[string]attr.Type{
	"content_fields":  types.ListType{ElemType: types.StringType},
	"filepath_field":  types.StringType,
	"title_field":     types.StringType,
	"url_field":       types.StringType,
	"vector_fields":   types.ListType{ElemType: types.StringType},
	"metadata_fields": types.ListType{ElemType: types.StringType},
}

var azureSearchIndexAttrTypes = map[string]attr.Type{
	"connection_name": types.StringType,
	"index_name":      types.StringType,
	"field_mapping":   types.ObjectType{AttrTypes: fieldMappingAttrTypes},
}

func (r *FoundryProjectIndexResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_index"
}

func (r *FoundryProjectIndexResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Registers an existing Azure AI Search index with the Foundry " +
			"project's index catalog — the **Foundry IQ → Indexes** tab in the portal.\n\n" +
			"Distinct from `azurefoundry_knowledge_source` / `azurefoundry_knowledge_base`: " +
			"those manage the Search-side data model (the **Knowledge bases** tab and the " +
			"agentic-retrieval MCP endpoint). This resource manages the project-side catalog " +
			"entry that powers the Indexes tab and the legacy `azure_ai_search` agent tool variant. " +
			"Without it, the Indexes tab stays empty even when the Search service has indexes " +
			"the agent successfully queries.\n\n" +
			"Hits the Foundry project data plane at " +
			"`PUT/GET/DELETE {project}/indexes/{name}/versions/{version}?api-version=v1`. The " +
			"Indexes API is technically versioned but most users want \"register this index\" " +
			"semantics — the resource pins `version=\"1\"` and treats Update as a merge-patch " +
			"upsert against that version.\n\n" +
			"### Required setup\n" +
			"- An `azurerm_search_service` (or equivalent on `pulumi-azure-native`) with the " +
			"index already created and populated.\n" +
			"- A project connection of category `CognitiveSearch` pointing at the Search " +
			"service. Manage it with " +
			"[`azurerm_cognitive_account_project_connection`]" +
			"(https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cognitive_account_project_connection) " +
			"or [`azure-native:cognitiveservices:Connection`]" +
			"(https://www.pulumi.com/registry/packages/azure-native/api-docs/cognitiveservices/connection/) — " +
			"this provider does not manage connections.\n" +
			"- The Foundry project's managed identity needs **Search Index Data Contributor** " +
			"and **Search Service Contributor** on the Search service for keyless retrieval at " +
			"agent runtime.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Foundry-assigned asset ID, or the resource name when no separate ID is returned.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Index registration name. Unique within the Foundry project. Changing this forces replacement.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"kind": schema.StringAttribute{
				MarkdownDescription: "Index type discriminator. Today only `AzureSearch` is supported. Changing this forces replacement.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(client.ProjectIndexTypeAzureSearch),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional human-readable description shown in the portal.",
				Optional:            true,
				Computed:            true,
			},
			"tags": schema.MapAttribute{
				MarkdownDescription: "Arbitrary key/value labels stored alongside the index registration.",
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
			},
			"azure_search": schema.SingleNestedAttribute{
				MarkdownDescription: "Variant params for `kind = \"AzureSearch\"`. Required.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"connection_name": schema.StringAttribute{
						MarkdownDescription: "Project connection of category `CognitiveSearch` that points at the underlying Search service. Pass the connection's *name*, not its full ARM ID — Foundry resolves it within the project scope.",
						Required:            true,
					},
					"index_name": schema.StringAttribute{
						MarkdownDescription: "Name of the existing index on the Search service. The provider does not create or populate the index itself — use `azurerm_search_index` upstream.",
						Required:            true,
					},
					"field_mapping": schema.SingleNestedAttribute{
						MarkdownDescription: "Optional column-rename / role-assignment envelope. Leave unset to use the index's own schema.",
						Optional:            true,
						Attributes: map[string]schema.Attribute{
							"content_fields": schema.ListAttribute{
								MarkdownDescription: "Fields whose textual content the agent retrieves.",
								Optional:            true,
								ElementType:         types.StringType,
							},
							"filepath_field": schema.StringAttribute{
								MarkdownDescription: "Field carrying the source file path / blob URL.",
								Optional:            true,
							},
							"title_field": schema.StringAttribute{
								MarkdownDescription: "Field carrying the human-readable title.",
								Optional:            true,
							},
							"url_field": schema.StringAttribute{
								MarkdownDescription: "Field carrying the canonical source URL for citations.",
								Optional:            true,
							},
							"vector_fields": schema.ListAttribute{
								MarkdownDescription: "Fields holding embedding vectors used for vector / hybrid search.",
								Optional:            true,
								ElementType:         types.StringType,
							},
							"metadata_fields": schema.ListAttribute{
								MarkdownDescription: "Additional fields surfaced as retrieval metadata.",
								Optional:            true,
								ElementType:         types.StringType,
							},
						},
					},
				},
			},
		},
	}
}

func (r *FoundryProjectIndexResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *FoundryProjectIndexResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FoundryProjectIndexResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wire, diags := buildProjectIndexWire(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating Foundry project index", map[string]any{
		"name": plan.Name.ValueString(),
		"kind": plan.Kind.ValueString(),
	})

	resp.Diagnostics.Append(r.preflightProjectIndexMustNotExist(ctx, plan.Name.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := r.client.CreateOrUpdateProjectIndex(ctx, wire)
	if err != nil {
		resp.Diagnostics.AddError("Error creating Foundry project index", err.Error())
		return
	}

	resp.Diagnostics.Append(applyProjectIndexResponse(ctx, result, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryProjectIndexResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FoundryProjectIndexResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := r.client.GetProjectIndex(ctx, state.Name.ValueString(), client.ProjectIndexDefaultVersion)
	if err != nil {
		if isNotFound(err) {
			tflog.Warn(ctx, "Foundry project index no longer exists, removing from state")
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Foundry project index", err.Error())
		return
	}

	resp.Diagnostics.Append(applyProjectIndexResponse(ctx, result, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryProjectIndexResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FoundryProjectIndexResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wire, diags := buildProjectIndexWire(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := r.client.CreateOrUpdateProjectIndex(ctx, wire)
	if err != nil {
		resp.Diagnostics.AddError("Error updating Foundry project index", err.Error())
		return
	}

	resp.Diagnostics.Append(applyProjectIndexResponse(ctx, result, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryProjectIndexResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FoundryProjectIndexResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteProjectIndex(ctx, state.Name.ValueString(), client.ProjectIndexDefaultVersion); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting Foundry project index", err.Error())
	}
}

func (r *FoundryProjectIndexResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	result, err := r.client.GetProjectIndex(ctx, req.ID, client.ProjectIndexDefaultVersion)
	if err != nil {
		resp.Diagnostics.AddError("Error importing Foundry project index", err.Error())
		return
	}

	var state FoundryProjectIndexResourceModel
	resp.Diagnostics.Append(applyProjectIndexResponse(ctx, result, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryProjectIndexResource) preflightProjectIndexMustNotExist(ctx context.Context, name string) diag.Diagnostics {
	var diags diag.Diagnostics
	existing, err := r.client.GetProjectIndex(ctx, name, client.ProjectIndexDefaultVersion)
	switch {
	case err == nil && existing != nil:
		summary, detail := alreadyExistsError(
			"project index", name,
			"azurefoundry_project_index", "azurefoundry:index:ProjectIndex",
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

func buildProjectIndexWire(ctx context.Context, m FoundryProjectIndexResourceModel) (client.ProjectIndex, diag.Diagnostics) {
	var diags diag.Diagnostics
	wire := client.ProjectIndex{
		Name:        m.Name.ValueString(),
		Type:        m.Kind.ValueString(),
		Version:     client.ProjectIndexDefaultVersion,
		Description: m.Description.ValueString(),
	}

	tags, d := extractMetadata(ctx, m.Tags)
	diags.Append(d...)
	wire.Tags = tags

	switch wire.Type {
	case client.ProjectIndexTypeAzureSearch:
		if m.AzureSearch.IsNull() || m.AzureSearch.IsUnknown() {
			diags.AddError("Missing azure_search block", "`kind = \"AzureSearch\"` requires an `azure_search` block.")
			return wire, diags
		}
		attrs := m.AzureSearch.Attributes()
		wire.ConnectionName = stringAttr(attrs, "connection_name")
		wire.IndexName = stringAttr(attrs, "index_name")
		if obj, ok := attrs["field_mapping"].(types.Object); ok && !obj.IsNull() && !obj.IsUnknown() {
			wire.FieldMapping = buildFieldMapping(ctx, obj)
		}
	default:
		diags.AddError("Unsupported project index kind", fmt.Sprintf("kind %q not supported by this provider", wire.Type))
	}
	return wire, diags
}

func buildFieldMapping(ctx context.Context, obj types.Object) *client.FieldMapping {
	attrs := obj.Attributes()
	fm := &client.FieldMapping{
		FilepathField: stringAttr(attrs, "filepath_field"),
		TitleField:    stringAttr(attrs, "title_field"),
		URLField:      stringAttr(attrs, "url_field"),
	}
	if v, ok := attrs["content_fields"].(types.List); ok {
		fm.ContentFields, _ = extractStringList(ctx, v)
	}
	if v, ok := attrs["vector_fields"].(types.List); ok {
		fm.VectorFields, _ = extractStringList(ctx, v)
	}
	if v, ok := attrs["metadata_fields"].(types.List); ok {
		fm.MetadataFields, _ = extractStringList(ctx, v)
	}
	return fm
}

func applyProjectIndexResponse(ctx context.Context, r *client.ProjectIndex, m *FoundryProjectIndexResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if r.ID != "" {
		m.ID = types.StringValue(r.ID)
	} else {
		m.ID = types.StringValue(r.Name)
	}
	m.Name = types.StringValue(r.Name)
	m.Kind = types.StringValue(r.Type)
	m.Description = types.StringValue(r.Description)

	if r.Tags != nil {
		tagAttrs := make(map[string]attr.Value, len(r.Tags))
		for k, v := range r.Tags {
			tagAttrs[k] = types.StringValue(v)
		}
		tagMap, d := types.MapValue(types.StringType, tagAttrs)
		diags.Append(d...)
		m.Tags = tagMap
	} else {
		m.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{})
	}

	switch r.Type {
	case client.ProjectIndexTypeAzureSearch:
		obj, d := wireAzureSearchToObject(ctx, r)
		diags.Append(d...)
		m.AzureSearch = obj
	default:
		m.AzureSearch = types.ObjectNull(azureSearchIndexAttrTypes)
	}
	return diags
}

func wireAzureSearchToObject(ctx context.Context, r *client.ProjectIndex) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	fm, d := wireFieldMappingToObject(ctx, r.FieldMapping)
	diags.Append(d...)
	obj, d := types.ObjectValue(azureSearchIndexAttrTypes, map[string]attr.Value{
		"connection_name": types.StringValue(r.ConnectionName),
		"index_name":      types.StringValue(r.IndexName),
		"field_mapping":   fm,
	})
	diags.Append(d...)
	return obj, diags
}

func wireFieldMappingToObject(_ context.Context, fm *client.FieldMapping) (types.Object, diag.Diagnostics) {
	if fm == nil {
		return types.ObjectNull(fieldMappingAttrTypes), nil
	}
	var diags diag.Diagnostics
	contentFields, d := stringSliceToList(fm.ContentFields)
	diags.Append(d...)
	vectorFields, d := stringSliceToList(fm.VectorFields)
	diags.Append(d...)
	metadataFields, d := stringSliceToList(fm.MetadataFields)
	diags.Append(d...)
	obj, d := types.ObjectValue(fieldMappingAttrTypes, map[string]attr.Value{
		"content_fields":  contentFields,
		"filepath_field":  types.StringValue(fm.FilepathField),
		"title_field":     types.StringValue(fm.TitleField),
		"url_field":       types.StringValue(fm.URLField),
		"vector_fields":   vectorFields,
		"metadata_fields": metadataFields,
	})
	diags.Append(d...)
	return obj, diags
}

func stringSliceToList(in []string) (types.List, diag.Diagnostics) {
	if in == nil {
		return types.ListNull(types.StringType), nil
	}
	values := make([]attr.Value, 0, len(in))
	for _, s := range in {
		values = append(values, types.StringValue(s))
	}
	return types.ListValue(types.StringType, values)
}
