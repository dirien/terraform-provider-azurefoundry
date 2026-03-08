// Copyright (c) Your Org
// SPDX-License-Identifier: MPL-2.0

package resources

import (
	"context"
	"fmt"

	"github.com/andrewCluey/terraform-provider-azurefoundry/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
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

var _ resource.Resource = &FoundryAgentResource{}
var _ resource.ResourceWithImportState = &FoundryAgentResource{}

func NewFoundryAgentResource() resource.Resource {
	return &FoundryAgentResource{}
}

type FoundryAgentResource struct {
	client *client.FoundryClient
}

type FoundryAgentResourceModel struct {
	ID                     types.String  `tfsdk:"id"`
	CreatedAt              types.Int64   `tfsdk:"created_at"`
	Model                  types.String  `tfsdk:"model"`
	Name                   types.String  `tfsdk:"name"`
	Description            types.String  `tfsdk:"description"`
	Instructions           types.String  `tfsdk:"instructions"`
	Temperature            types.Float64 `tfsdk:"temperature"`
	TopP                   types.Float64 `tfsdk:"top_p"`
	Metadata               types.Map     `tfsdk:"metadata"`
	Tools                  types.List    `tfsdk:"tools"`
	CodeInterpreterFileIDs types.List    `tfsdk:"code_interpreter_file_ids"`
	FileSearchVectorStoreIDs types.List  `tfsdk:"file_search_vector_store_ids"`
}

type toolModel struct {
	Type types.String `tfsdk:"type"`
}

var toolAttrTypes = map[string]attr.Type{
	"type": types.StringType,
}

func (r *FoundryAgentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (r *FoundryAgentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Azure AI Foundry Agent.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the agent.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.Int64Attribute{
				MarkdownDescription: "Unix timestamp when the agent was created.",
				Computed:            true,
			},
			"model": schema.StringAttribute{
				MarkdownDescription: "The model deployment name (e.g. `gpt-4o`, `gpt-4o-mini`).",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 256),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Display name for the agent.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(256),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "A short description of the agent.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(512),
				},
			},
			"instructions": schema.StringAttribute{
				MarkdownDescription: "The system prompt for the agent.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(256000),
				},
			},
			"temperature": schema.Float64Attribute{ 
				MarkdownDescription: "Sampling temperature between 0 and 2.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.Float64{
					float64validator.Between(0.0, 2.0),
				}, // default of 1.0
			},
			"top_p": schema.Float64Attribute{
				MarkdownDescription: "Nucleus sampling parameter between 0 and 1.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.Float64{
					float64validator.Between(0.0, 1.0),
				},
			},
			"metadata": schema.MapAttribute{
				MarkdownDescription: "Up to 16 key/value string pairs.",
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
				Validators: []validator.Map{
					mapvalidator.SizeBetween(0, 16),
					mapvalidator.KeysAre(stringvalidator.LengthAtMost(512)),
				},
			},
			"code_interpreter_file_ids": schema.ListAttribute{
				MarkdownDescription: "File IDs available to the code_interpreter tool.",
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
				Validators: []validator.List{
					listvalidator.SizeBetween(0, 20),
				},
			},
			"file_search_vector_store_ids": schema.ListAttribute{
				MarkdownDescription: "Vector store IDs (from `azurefoundry_vector_store`) to attach to the `file_search` tool. Maximum 1.",
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
				Validators: []validator.List{
					listvalidator.SizeBetween(0, 1),
					listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"tools": schema.ListNestedBlock{
				MarkdownDescription: "Tools enabled for the agent.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							MarkdownDescription: "Tool type: `code_interpreter`, `file_search`, `bing_grounding`, `azure_ai_search`, `azure_function`.",
							Required:            true,
							Validators: []validator.String{
								stringvalidator.OneOf(
									"code_interpreter",
									"file_search",
									"bing_grounding",
									"azure_ai_search",
									"azure_function",
								),
							},
						},
					},
				},
			},
		},
	}
}

func (r *FoundryAgentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *FoundryAgentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FoundryAgentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, diags := modelToCreateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating Foundry agent", map[string]interface{}{"name": apiReq.Name, "model": apiReq.Model})

	agentResp, err := r.client.CreateAgent(ctx, apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Error creating Foundry agent", err.Error())
		return
	}

	resp.Diagnostics.Append(responseToModel(ctx, agentResp, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryAgentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FoundryAgentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	agentResp, err := r.client.GetAgent(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			tflog.Warn(ctx, "Foundry agent no longer exists, removing from state")
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Foundry agent", err.Error())
		return
	}

	resp.Diagnostics.Append(responseToModel(ctx, agentResp, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryAgentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FoundryAgentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	var state FoundryAgentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, diags := modelToUpdateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Updating Foundry agent", map[string]interface{}{"id": state.ID.ValueString()})

	agentResp, err := r.client.UpdateAgent(ctx, state.ID.ValueString(), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Error updating Foundry agent", err.Error())
		return
	}

	resp.Diagnostics.Append(responseToModel(ctx, agentResp, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryAgentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FoundryAgentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting Foundry agent", map[string]interface{}{"id": state.ID.ValueString()})

	_, err := r.client.DeleteAgent(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting Foundry agent", err.Error())
		return
	}
}

func (r *FoundryAgentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	agentResp, err := r.client.GetAgent(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing Foundry agent", err.Error())
		return
	}

	var state FoundryAgentResourceModel
	resp.Diagnostics.Append(responseToModel(ctx, agentResp, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ─────────────────────────────────────────────────────────────────────────────
// Mapping helpers
// ─────────────────────────────────────────────────────────────────────────────

func modelToCreateRequest(ctx context.Context, m FoundryAgentResourceModel) (client.CreateAgentRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	req := client.CreateAgentRequest{
		Model:        m.Model.ValueString(),
		Name:         m.Name.ValueString(),
		Description:  m.Description.ValueString(),
		Instructions: m.Instructions.ValueString(),
	}

	if !m.Temperature.IsNull() && !m.Temperature.IsUnknown() {
		v := m.Temperature.ValueFloat64()
		req.Temperature = &v
	}
	if !m.TopP.IsNull() && !m.TopP.IsUnknown() {
		v := m.TopP.ValueFloat64()
		req.TopP = &v
	}

	if !m.Metadata.IsNull() && !m.Metadata.IsUnknown() {
		meta := make(map[string]types.String, len(m.Metadata.Elements()))
		diags.Append(m.Metadata.ElementsAs(ctx, &meta, false)...)
		metadata := make(map[string]string, len(meta))
		for k, v := range meta {
			metadata[k] = v.ValueString()
		}
		req.Metadata = metadata
	}

	tools, d := extractTools(ctx, m.Tools)
	diags.Append(d...)
	req.Tools = tools

	if !m.CodeInterpreterFileIDs.IsNull() && !m.CodeInterpreterFileIDs.IsUnknown() {
		var fileIDs []string
		diags.Append(m.CodeInterpreterFileIDs.ElementsAs(ctx, &fileIDs, false)...)
		if len(fileIDs) > 0 {
			if req.ToolResources == nil {
				req.ToolResources = &client.ToolResources{}
			}
			req.ToolResources.CodeInterpreter = &client.CodeInterpreterResources{FileIDs: fileIDs}
		}
	}

	if !m.FileSearchVectorStoreIDs.IsNull() && !m.FileSearchVectorStoreIDs.IsUnknown() {
		var vsIDs []string
		diags.Append(m.FileSearchVectorStoreIDs.ElementsAs(ctx, &vsIDs, false)...)
		if len(vsIDs) > 0 {
			if req.ToolResources == nil {
				req.ToolResources = &client.ToolResources{}
			}
			req.ToolResources.FileSearch = &client.FileSearchResources{VectorStoreIDs: vsIDs}
		}
	}

	return req, diags
}

func modelToUpdateRequest(ctx context.Context, m FoundryAgentResourceModel) (client.UpdateAgentRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	req := client.UpdateAgentRequest{
		Model:        m.Model.ValueString(),
		Name:         m.Name.ValueString(),
		Description:  m.Description.ValueString(),
		Instructions: m.Instructions.ValueString(),
	}

	if !m.Temperature.IsNull() && !m.Temperature.IsUnknown() {
		v := m.Temperature.ValueFloat64()
		req.Temperature = &v
	}
	if !m.TopP.IsNull() && !m.TopP.IsUnknown() {
		v := m.TopP.ValueFloat64()
		req.TopP = &v
	}

	if !m.Metadata.IsNull() && !m.Metadata.IsUnknown() {
		meta := make(map[string]types.String, len(m.Metadata.Elements()))
		diags.Append(m.Metadata.ElementsAs(ctx, &meta, false)...)
		metadata := make(map[string]string, len(meta))
		for k, v := range meta {
			metadata[k] = v.ValueString()
		}
		req.Metadata = metadata
	}

	tools, d := extractTools(ctx, m.Tools)
	diags.Append(d...)
	req.Tools = tools

	if !m.CodeInterpreterFileIDs.IsNull() && !m.CodeInterpreterFileIDs.IsUnknown() {
		var fileIDs []string
		diags.Append(m.CodeInterpreterFileIDs.ElementsAs(ctx, &fileIDs, false)...)
		if len(fileIDs) > 0 {
			if req.ToolResources == nil {
				req.ToolResources = &client.ToolResources{}
			}
			req.ToolResources.CodeInterpreter = &client.CodeInterpreterResources{FileIDs: fileIDs}
		}
	}

	if !m.FileSearchVectorStoreIDs.IsNull() && !m.FileSearchVectorStoreIDs.IsUnknown() {
		var vsIDs []string
		diags.Append(m.FileSearchVectorStoreIDs.ElementsAs(ctx, &vsIDs, false)...)
		if len(vsIDs) > 0 {
			if req.ToolResources == nil {
				req.ToolResources = &client.ToolResources{}
			}
			req.ToolResources.FileSearch = &client.FileSearchResources{VectorStoreIDs: vsIDs}
		}
	}

	return req, diags
}

func responseToModel(ctx context.Context, r *client.AgentResponse, m *FoundryAgentResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics

	m.ID = types.StringValue(r.ID)
	m.CreatedAt = types.Int64Value(r.CreatedAt)
	m.Model = types.StringValue(r.Model)
	m.Name = types.StringValue(r.Name)
	m.Description = types.StringValue(r.Description)
	m.Instructions = types.StringValue(r.Instructions)

	if r.Temperature != nil {
		m.Temperature = types.Float64Value(*r.Temperature)
	} else {
		m.Temperature = types.Float64Null()
	}
	if r.TopP != nil {
		m.TopP = types.Float64Value(*r.TopP)
	} else {
		m.TopP = types.Float64Null()
	}

	if r.Metadata != nil {
		metaAttrs := make(map[string]attr.Value, len(r.Metadata))
		for k, v := range r.Metadata {
			metaAttrs[k] = types.StringValue(v)
		}
		metaMap, d := types.MapValue(types.StringType, metaAttrs)
		diags.Append(d...)
		m.Metadata = metaMap
	} else {
		m.Metadata = types.MapValueMust(types.StringType, map[string]attr.Value{})
	}

	toolObjects := make([]attr.Value, 0, len(r.Tools))
	for _, t := range r.Tools {
		toolMap, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		toolType, _ := toolMap["type"].(string)
		obj, d := types.ObjectValue(toolAttrTypes, map[string]attr.Value{
			"type": types.StringValue(toolType),
		})
		diags.Append(d...)
		toolObjects = append(toolObjects, obj)
	}
	toolList, d := types.ListValue(types.ObjectType{AttrTypes: toolAttrTypes}, toolObjects)
	diags.Append(d...)
	m.Tools = toolList

	if r.ToolResources != nil && r.ToolResources.CodeInterpreter != nil {
		fileIDVals := make([]attr.Value, len(r.ToolResources.CodeInterpreter.FileIDs))
		for i, id := range r.ToolResources.CodeInterpreter.FileIDs {
			fileIDVals[i] = types.StringValue(id)
		}
		fileList, d := types.ListValue(types.StringType, fileIDVals)
		diags.Append(d...)
		m.CodeInterpreterFileIDs = fileList
	} else {
		m.CodeInterpreterFileIDs = types.ListValueMust(types.StringType, []attr.Value{})
	}

	if r.ToolResources != nil && r.ToolResources.FileSearch != nil {
		vsIDVals := make([]attr.Value, len(r.ToolResources.FileSearch.VectorStoreIDs))
		for i, id := range r.ToolResources.FileSearch.VectorStoreIDs {
			vsIDVals[i] = types.StringValue(id)
		}
		vsList, d := types.ListValue(types.StringType, vsIDVals)
		diags.Append(d...)
		m.FileSearchVectorStoreIDs = vsList
	} else {
		m.FileSearchVectorStoreIDs = types.ListValueMust(types.StringType, []attr.Value{})
	}

	return diags
}

func extractTools(ctx context.Context, toolsList types.List) ([]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics
	if toolsList.IsNull() || toolsList.IsUnknown() {
		return nil, diags
	}

	var toolModels []toolModel
	diags.Append(toolsList.ElementsAs(ctx, &toolModels, false)...)
	if diags.HasError() {
		return nil, diags
	}

	tools := make([]interface{}, len(toolModels))
	for i, t := range toolModels {
		tools[i] = client.ToolDefinition{Type: t.Type.ValueString()}
	}
	return tools, diags
}

func isNotFound(err error) bool {
	apiErr, ok := err.(*client.APIError)
	return ok && apiErr.StatusCode == 404
}