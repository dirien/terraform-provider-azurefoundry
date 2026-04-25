// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/dirien/terraform-provider-azurefoundry/internal/client"

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

var (
	_ resource.Resource                = &FoundryAgentResource{}
	_ resource.ResourceWithImportState = &FoundryAgentResource{}
)

func NewFoundryAgentResource() resource.Resource {
	return &FoundryAgentResource{}
}

type FoundryAgentResource struct {
	client *client.FoundryClient
}

type FoundryAgentResourceModel struct {
	ID                       types.String  `tfsdk:"id"`
	CreatedAt                types.Int64   `tfsdk:"created_at"`
	Model                    types.String  `tfsdk:"model"`
	Name                     types.String  `tfsdk:"name"`
	Description              types.String  `tfsdk:"description"`
	Instructions             types.String  `tfsdk:"instructions"`
	Temperature              types.Float64 `tfsdk:"temperature"`
	TopP                     types.Float64 `tfsdk:"top_p"`
	Metadata                 types.Map     `tfsdk:"metadata"`
	Tools                    types.List    `tfsdk:"tools"`
	CodeInterpreterFileIDs   types.List    `tfsdk:"code_interpreter_file_ids"`
	FileSearchVectorStoreIDs types.List    `tfsdk:"file_search_vector_store_ids"`
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

	tflog.Debug(ctx, "Creating Foundry agent", map[string]any{"name": apiReq.Name, "model": apiReq.Model})

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

	tflog.Debug(ctx, "Updating Foundry agent", map[string]any{"id": state.ID.ValueString()})

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

	tflog.Debug(ctx, "Deleting Foundry agent", map[string]any{"id": state.ID.ValueString()})

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

// agentRequestFields holds the wire-format fields shared by Create and Update
// agent requests. CreateAgentRequest and UpdateAgentRequest only differ in
// presence of `Model` on Create (it's required there) — the rest is identical.
type agentRequestFields struct {
	Model         string
	Name          string
	Description   string
	Instructions  string
	Tools         []any
	ToolResources *client.ToolResources
	Temperature   *float64
	TopP          *float64
	Metadata      map[string]string
}

func modelToCreateRequest(ctx context.Context, m FoundryAgentResourceModel) (client.CreateAgentRequest, diag.Diagnostics) {
	f, diags := buildAgentRequestFields(ctx, m)
	return client.CreateAgentRequest{
		Model:         f.Model,
		Name:          f.Name,
		Description:   f.Description,
		Instructions:  f.Instructions,
		Tools:         f.Tools,
		ToolResources: f.ToolResources,
		Temperature:   f.Temperature,
		TopP:          f.TopP,
		Metadata:      f.Metadata,
	}, diags
}

func modelToUpdateRequest(ctx context.Context, m FoundryAgentResourceModel) (client.UpdateAgentRequest, diag.Diagnostics) {
	f, diags := buildAgentRequestFields(ctx, m)
	return client.UpdateAgentRequest{
		Model:         f.Model,
		Name:          f.Name,
		Description:   f.Description,
		Instructions:  f.Instructions,
		Tools:         f.Tools,
		ToolResources: f.ToolResources,
		Temperature:   f.Temperature,
		TopP:          f.TopP,
		Metadata:      f.Metadata,
	}, diags
}

func buildAgentRequestFields(ctx context.Context, m FoundryAgentResourceModel) (agentRequestFields, diag.Diagnostics) {
	var diags diag.Diagnostics
	f := agentRequestFields{
		Model:        m.Model.ValueString(),
		Name:         m.Name.ValueString(),
		Description:  m.Description.ValueString(),
		Instructions: m.Instructions.ValueString(),
		Temperature:  optionalFloat64(m.Temperature),
		TopP:         optionalFloat64(m.TopP),
	}

	meta, d := extractMetadata(ctx, m.Metadata)
	diags.Append(d...)
	f.Metadata = meta

	tools, d := extractTools(ctx, m.Tools)
	diags.Append(d...)
	f.Tools = tools

	tr, d := buildToolResources(ctx, m)
	diags.Append(d...)
	f.ToolResources = tr

	return f, diags
}

// optionalFloat64 returns a heap-allocated copy when the framework value is
// set, or nil when it is null/unknown — matches the omitempty wire shape.
func optionalFloat64(v types.Float64) *float64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	out := v.ValueFloat64()
	return &out
}

func extractMetadata(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return nil, nil
	}
	raw := make(map[string]types.String, len(m.Elements()))
	diags := m.ElementsAs(ctx, &raw, false)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = v.ValueString()
	}
	return out, diags
}

func buildToolResources(ctx context.Context, m FoundryAgentResourceModel) (*client.ToolResources, diag.Diagnostics) {
	var diags diag.Diagnostics
	var tr *client.ToolResources

	if fileIDs, d := extractStringList(ctx, m.CodeInterpreterFileIDs); len(fileIDs) > 0 {
		diags.Append(d...)
		tr = ensureToolResources(tr)
		tr.CodeInterpreter = &client.CodeInterpreterResources{FileIDs: fileIDs}
	} else {
		diags.Append(d...)
	}

	if vsIDs, d := extractStringList(ctx, m.FileSearchVectorStoreIDs); len(vsIDs) > 0 {
		diags.Append(d...)
		tr = ensureToolResources(tr)
		tr.FileSearch = &client.FileSearchResources{VectorStoreIDs: vsIDs}
	} else {
		diags.Append(d...)
	}

	return tr, diags
}

func extractStringList(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	d := l.ElementsAs(ctx, &out, false)
	return out, d
}

func ensureToolResources(tr *client.ToolResources) *client.ToolResources {
	if tr == nil {
		return &client.ToolResources{}
	}
	return tr
}

func responseToModel(_ context.Context, r *client.AgentResponse, m *FoundryAgentResourceModel) diag.Diagnostics {
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
		toolMap, ok := t.(map[string]any)
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

func extractTools(ctx context.Context, toolsList types.List) ([]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	if toolsList.IsNull() || toolsList.IsUnknown() {
		return nil, diags
	}

	var toolModels []toolModel
	diags.Append(toolsList.ElementsAs(ctx, &toolModels, false)...)
	if diags.HasError() {
		return nil, diags
	}

	tools := make([]any, len(toolModels))
	for i, t := range toolModels {
		tools[i] = client.ToolDefinition{Type: t.Type.ValueString()}
	}
	return tools, diags
}

func isNotFound(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// isConflict reports whether err is an HTTP 409 from the Foundry API.
// 409 is what the data plane returns when a resource keyed by a
// user-supplied name (agent, memory store) already exists. Usually that
// means an orphan from a prior create that didn't make it into Pulumi /
// Terraform state — provider crash, signal interrupt, or a state-write
// hiccup leaving the data-plane resource live but unmanaged.
func isConflict(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

// alreadyExistsError formats a recovery message for the orphan case.
// Resource is the human label (e.g. "agent", "memory store"), name is the
// user-supplied identifier, and tfType / pulumiType are the import-time
// resource references for each frontend.
func alreadyExistsError(resourceLabel, name, tfType, pulumiType string) (summary, detail string) {
	summary = fmt.Sprintf("Foundry %s %q already exists", resourceLabel, name)
	detail = fmt.Sprintf(
		"A %s named %q exists in the Foundry project but is not tracked in "+
			"this Terraform/Pulumi state. This usually means a prior create "+
			"succeeded server-side but the result was not recorded in state "+
			"(provider crash, signal interrupt, or network blip during state "+
			"write).\n\n"+
			"Recover with one of:\n\n"+
			"  Terraform:  terraform import %s.<address> %s\n"+
			"  Pulumi:     pulumi import %s <pulumi-name> %s\n\n"+
			"Then re-run apply/up to reconcile drift. To force a clean "+
			"recreation, delete the existing %s in the Foundry project first.",
		resourceLabel, name, tfType, name, pulumiType, name, resourceLabel,
	)
	return summary, detail
}
