// Copyright (c) Your Org
// SPDX-License-Identifier: MPL-2.0

package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

var _ resource.Resource = &FoundryAgentV2Resource{}
var _ resource.ResourceWithImportState = &FoundryAgentV2Resource{}

func NewFoundryAgentV2Resource() resource.Resource {
	return &FoundryAgentV2Resource{}
}

type FoundryAgentV2Resource struct {
	client *client.FoundryClient
}

type FoundryAgentV2ResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	Description          types.String `tfsdk:"description"`
	CreatedAt            types.Int64  `tfsdk:"created_at"`
	Version              types.String `tfsdk:"version"`
	Metadata             types.Map    `tfsdk:"metadata"`
	Kind                 types.String `tfsdk:"kind"`
	Model                types.String `tfsdk:"model"`
	Instructions         types.String `tfsdk:"instructions"`
	StructuredInputsJSON types.String `tfsdk:"structured_inputs_json"`
	Tools                types.List   `tfsdk:"tools"`

	// Hosted-agent fields. Used only when kind is "container_app" or "hosted".
	Image                     types.String `tfsdk:"image"`
	Cpu                       types.String `tfsdk:"cpu"`
	Memory                    types.String `tfsdk:"memory"`
	ContainerProtocolVersions types.List   `tfsdk:"container_protocol_versions"`
	EnvironmentVariables      types.Map    `tfsdk:"environment_variables"`

	// Warmup: when true on a kind="hosted" agent, Create blocks until the
	// agent's Responses endpoint stops returning HTTP 424
	// (session_not_ready). Defaults to false. No-op for kind="prompt".
	Warmup        types.Bool   `tfsdk:"warmup"`
	WarmupTimeout types.String `tfsdk:"warmup_timeout"`

	// Computed: Foundry-managed Entra identity Foundry creates for each
	// hosted-agent version. Grant `Azure AI User` to this identity for the
	// container to authenticate to the model at runtime.
	InstanceIdentity types.Object `tfsdk:"instance_identity"`
}

var instanceIdentityAttrTypes = map[string]attr.Type{
	"client_id":    types.StringType,
	"principal_id": types.StringType,
}

var protocolVersionAttrTypes = map[string]attr.Type{
	"protocol": types.StringType,
	"version":  types.StringType,
}

// toolModelV2 mirrors one element of the `tools` list block. Variant configs
// are SingleNestedAttribute so they expose as typed nested objects through the
// Pulumi bridge instead of opaque JSON strings.
type toolModelV2 struct {
	Type           types.String `tfsdk:"type"`
	VectorStoreIDs types.List   `tfsdk:"vector_store_ids"`
	MaxNumResults  types.Int64  `tfsdk:"max_num_results"`

	CodeInterpreter types.Object `tfsdk:"code_interpreter"`
	Function        types.Object `tfsdk:"function"`
	OpenAPI         types.Object `tfsdk:"openapi"`
	MCP             types.Object `tfsdk:"mcp"`
	AzureAISearch   types.Object `tfsdk:"azure_ai_search"`
	BingGrounding   types.Object `tfsdk:"bing_grounding"`
	MemorySearch    types.Object `tfsdk:"memory_search"`
}

// ── Tool variant attribute type maps ────────────────────────────────────────

var codeInterpreterAttrTypes = map[string]attr.Type{
	"file_ids": types.ListType{ElemType: types.StringType},
}

var functionAttrTypes = map[string]attr.Type{
	"name":            types.StringType,
	"description":     types.StringType,
	"parameters_json": types.StringType,
}

var openapiAttrTypes = map[string]attr.Type{
	"name":        types.StringType,
	"description": types.StringType,
	"spec_json":   types.StringType,
	"auth_type":   types.StringType,
}

var mcpAttrTypes = map[string]attr.Type{
	"server_label":          types.StringType,
	"server_url":            types.StringType,
	"require_approval":      types.StringType,
	"project_connection_id": types.StringType,
}

var azureAISearchIndexAttrTypes = map[string]attr.Type{
	"project_connection_id": types.StringType,
	"index_name":            types.StringType,
	"query_type":            types.StringType,
	"top_k":                 types.Int64Type,
}

var azureAISearchAttrTypes = map[string]attr.Type{
	"indexes": types.ListType{ElemType: types.ObjectType{AttrTypes: azureAISearchIndexAttrTypes}},
}

var bingGroundingAttrTypes = map[string]attr.Type{
	"connection_id": types.StringType,
}

var memorySearchAttrTypes = map[string]attr.Type{
	"memory_store_name": types.StringType,
	"scope":             types.StringType,
	"update_delay":      types.Int64Type,
}

var toolAttrTypesV2 = map[string]attr.Type{
	"type":             types.StringType,
	"vector_store_ids": types.ListType{ElemType: types.StringType},
	"max_num_results":  types.Int64Type,
	"code_interpreter": types.ObjectType{AttrTypes: codeInterpreterAttrTypes},
	"function":         types.ObjectType{AttrTypes: functionAttrTypes},
	"openapi":          types.ObjectType{AttrTypes: openapiAttrTypes},
	"mcp":              types.ObjectType{AttrTypes: mcpAttrTypes},
	"azure_ai_search":  types.ObjectType{AttrTypes: azureAISearchAttrTypes},
	"bing_grounding":   types.ObjectType{AttrTypes: bingGroundingAttrTypes},
	"memory_search":    types.ObjectType{AttrTypes: memorySearchAttrTypes},
}

func (r *FoundryAgentV2Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_v2"
}

func (r *FoundryAgentV2Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Azure AI Foundry Agent (v2 API).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
			},
			"created_at": schema.Int64Attribute{
				Computed: true,
			},
			"version": schema.StringAttribute{
				Computed: true,
			},
			"metadata": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
			},
			"kind": schema.StringAttribute{
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf("prompt", "hosted", "container_app", "workflow"),
				},
			},
			"model": schema.StringAttribute{
				Required: true,
			},
			"instructions": schema.StringAttribute{
				Optional: true,
				Computed: true,
			},
			"structured_inputs_json": schema.StringAttribute{
				MarkdownDescription: "JSON string describing the agent's `structured_inputs` schema. " +
					"Use `jsonencode({...})` in HCL or `json.dumps({...})` in a Pulumi program.",
				Optional: true,
			},
			"image": schema.StringAttribute{
				MarkdownDescription: "Container image URL including tag. Required when `kind` is `container_app` or `hosted`; ignored for `prompt`. Example: `myacr.azurecr.io/fraud-agent:0.1.0`.",
				Optional:            true,
			},
			"cpu": schema.StringAttribute{
				MarkdownDescription: "vCPU allocation as a string (e.g. `1`, `2`). Required for `container_app`/`hosted` kinds. Allowed pairs: see the Foundry hosted-agent size matrix.",
				Optional:            true,
			},
			"memory": schema.StringAttribute{
				MarkdownDescription: "Memory allocation in GiB as a string (e.g. `2Gi`, `4Gi`). Required for `container_app`/`hosted` kinds.",
				Optional:            true,
			},
			"instance_identity": schema.SingleNestedAttribute{
				MarkdownDescription: "Foundry-managed Entra identity for a hosted-agent version. Computed. Use `client_id` / `principal_id` to grant runtime RBAC (e.g. `Azure AI User` on the Foundry account).",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"client_id":    schema.StringAttribute{Computed: true},
					"principal_id": schema.StringAttribute{Computed: true},
				},
			},
			"container_protocol_versions": schema.ListNestedAttribute{
				MarkdownDescription: "Protocols the container speaks. Required for `container_app`/`hosted` kinds. Today the valid protocols are `responses` (Azure OpenAI Responses API) and `a2a` (Agent-to-Agent).",
				Optional:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"protocol": schema.StringAttribute{Required: true},
						"version":  schema.StringAttribute{Required: true},
					},
				},
			},
			"environment_variables": schema.MapAttribute{
				MarkdownDescription: "Env vars injected into the hosted agent container. Do not put secrets here — use a connection to a secret store instead.",
				Optional:            true,
				ElementType:         types.StringType,
			},
			"warmup": schema.BoolAttribute{
				MarkdownDescription: "When `true` on a `kind=\"hosted\"` agent, Create blocks until the per-session sandbox responds non-424 on its Responses endpoint. Lets dependent resources rely on the agent being immediately reachable. No-op for `kind=\"prompt\"`. Defaults to `false`.",
				Optional:            true,
			},
			"warmup_timeout": schema.StringAttribute{
				MarkdownDescription: "Maximum time to wait for the agent to become ready when `warmup=true`. Go duration string (e.g. `5m`, `90s`). Defaults to `5m`.",
				Optional:            true,
			},
		},
		Blocks: map[string]schema.Block{
			"tools": schema.ListNestedBlock{
				MarkdownDescription: "Tools enabled for the agent.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Required: true,
							Validators: []validator.String{
								stringvalidator.OneOf(
									"file_search",
									"code_interpreter",
									"web_search",
									"bing_grounding",
									"function",
									"openapi",
									"mcp",
									"azure_ai_search",
									"memory_search",
								),
							},
						},
						"vector_store_ids": schema.ListAttribute{
							Optional:    true,
							ElementType: types.StringType,
						},
						"max_num_results": schema.Int64Attribute{
							Optional: true,
						},
						"code_interpreter": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"file_ids": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
								},
							},
						},
						"function": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"name":            schema.StringAttribute{Required: true},
								"description":     schema.StringAttribute{Optional: true},
								"parameters_json": schema.StringAttribute{Optional: true},
							},
						},
						"openapi": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"name":        schema.StringAttribute{Required: true},
								"description": schema.StringAttribute{Optional: true},
								"spec_json":   schema.StringAttribute{Required: true},
								"auth_type":   schema.StringAttribute{Optional: true},
							},
						},
						"mcp": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"server_label":          schema.StringAttribute{Required: true},
								"server_url":            schema.StringAttribute{Required: true},
								"require_approval":      schema.StringAttribute{Optional: true},
								"project_connection_id": schema.StringAttribute{Optional: true},
							},
						},
						"azure_ai_search": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"indexes": schema.ListNestedAttribute{
									Required: true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"project_connection_id": schema.StringAttribute{Required: true},
											"index_name":            schema.StringAttribute{Required: true},
											"query_type":            schema.StringAttribute{Optional: true},
											"top_k":                 schema.Int64Attribute{Optional: true},
										},
									},
								},
							},
						},
						"bing_grounding": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"connection_id": schema.StringAttribute{Required: true},
							},
						},
						"memory_search": schema.SingleNestedAttribute{
							MarkdownDescription: "Attach a Foundry Memory store (preview). Wire type is `memory_search_preview`; provider accepts the shorter `memory_search` for forward-compat.",
							Optional:            true,
							Attributes: map[string]schema.Attribute{
								"memory_store_name": schema.StringAttribute{Required: true},
								"scope":             schema.StringAttribute{Optional: true},
								"update_delay":      schema.Int64Attribute{Optional: true},
							},
						},
					},
				},
			},
		},
	}
}

func (r *FoundryAgentV2Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *FoundryAgentV2Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FoundryAgentV2ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, diags := modelToCreateV2Request(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating Foundry agent", map[string]interface{}{"name": apiReq.Name, "model": apiReq.Definition.Model})

	// Block until the project's data plane is reachable (project routing +
	// RBAC propagation). First Create per session pays the cost; the rest
	// short-circuit via a cached flag.
	if err := r.client.WaitForProjectReady(ctx, 30*time.Minute); err != nil {
		resp.Diagnostics.AddError("Foundry project not reachable", err.Error())
		return
	}

	// Pre-flight GET: shrink the orphan-creation race. If a resource with
	// this name already exists in the data plane, fail with the import
	// hint *before* we POST. Without this, a Create that's about to 409
	// would still return that 409 — but the orphan was created by an
	// earlier run, not by us. With this, we can also catch the case where
	// state was lost (e.g. backend wipe) and the data-plane resource is
	// still here. The remaining race window — between this GET and the
	// POST below — is on the order of one HTTP roundtrip, and a true
	// concurrent create from another caller would still surface the same
	// import-hint error (caught below by isConflict).
	if existing, getErr := r.client.GetAgentV2(ctx, apiReq.Name); getErr == nil && existing != nil {
		summary, detail := alreadyExistsError(
			"agent", apiReq.Name,
			"azurefoundry_agent_v2", "azurefoundry:index:AgentV2",
		)
		resp.Diagnostics.AddError(summary, detail)
		return
	} else if getErr != nil && !isNotFound(getErr) {
		resp.Diagnostics.AddError("Pre-flight existence check failed", getErr.Error())
		return
	}

	agentResp, err := r.client.CreateAgentV2(ctx, apiReq)
	if err != nil {
		if isConflict(err) {
			summary, detail := alreadyExistsError(
				"agent", apiReq.Name,
				"azurefoundry_agent_v2", "azurefoundry:index:AgentV2",
			)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		resp.Diagnostics.AddError("Error creating Foundry agent", err.Error())
		return
	}

	resp.Diagnostics.Append(responseToV2Model(ctx, agentResp, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Warmup is opt-in and only meaningful for hosted agents — prompt
	// agents are reachable as soon as Create returns (no per-session
	// sandbox to cold-start).
	wantWarmup := !plan.Warmup.IsNull() && !plan.Warmup.IsUnknown() && plan.Warmup.ValueBool()
	isHosted := plan.Kind.ValueString() == "hosted" || plan.Kind.ValueString() == "container_app"
	if wantWarmup && isHosted {
		timeout := 5 * time.Minute
		if !plan.WarmupTimeout.IsNull() && !plan.WarmupTimeout.IsUnknown() {
			if d, derr := time.ParseDuration(plan.WarmupTimeout.ValueString()); derr == nil && d > 0 {
				timeout = d
			} else if derr != nil {
				resp.Diagnostics.AddError(
					"Invalid warmup_timeout",
					fmt.Sprintf("warmup_timeout %q is not a valid Go duration: %s", plan.WarmupTimeout.ValueString(), derr.Error()),
				)
				return
			}
		}
		tflog.Debug(ctx, "Warming up Foundry agent", map[string]interface{}{"name": apiReq.Name, "timeout": timeout.String()})
		if err := r.client.WaitForAgentV2Ready(ctx, apiReq.Name, timeout, 5*time.Second); err != nil {
			resp.Diagnostics.AddError("Agent warmup failed", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryAgentV2Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FoundryAgentV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	agentResp, err := r.client.GetAgentV2(ctx, state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			tflog.Warn(ctx, "Foundry agent no longer exists, removing from state")
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Foundry agent", err.Error())
		return
	}

	resp.Diagnostics.Append(responseToV2Model(ctx, agentResp, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryAgentV2Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FoundryAgentV2ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	var state FoundryAgentV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, diags := modelToUpdateV2Request(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Updating Foundry agent", map[string]interface{}{"id": state.Name.ValueString()})

	agentResp, err := r.client.UpdateAgentV2(ctx, state.Name.ValueString(), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Error updating Foundry agent", err.Error())
		return
	}

	resp.Diagnostics.Append(responseToV2Model(ctx, agentResp, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryAgentV2Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FoundryAgentV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting Foundry agent", map[string]interface{}{"id": state.Name.ValueString()})

	_, err := r.client.DeleteAgentV2(ctx, state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting Foundry agent", err.Error())
		return
	}
}

func (r *FoundryAgentV2Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	agentResp, err := r.client.GetAgentV2(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing Foundry agent", err.Error())
		return
	}

	var state FoundryAgentV2ResourceModel
	resp.Diagnostics.Append(responseToV2Model(ctx, agentResp, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ─────────────────────────────────────────────────────────────────────────────
// Mapping helpers
// ─────────────────────────────────────────────────────────────────────────────

func modelToCreateV2Request(ctx context.Context, m FoundryAgentV2ResourceModel) (client.CreateAgentV2Request, diag.Diagnostics) {
	var diags diag.Diagnostics
	def, d := buildAgentDefinition(ctx, m)
	diags.Append(d...)

	req := client.CreateAgentV2Request{
		Name:        m.Name.ValueString(),
		Description: m.Description.ValueString(),
		Definition:  def,
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
	return req, diags
}

func modelToUpdateV2Request(ctx context.Context, m FoundryAgentV2ResourceModel) (client.UpdateAgentV2Request, diag.Diagnostics) {
	var diags diag.Diagnostics
	def, d := buildAgentDefinition(ctx, m)
	diags.Append(d...)

	req := client.UpdateAgentV2Request{
		Description: m.Description.ValueString(),
		Definition:  def,
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
	return req, diags
}

func buildAgentDefinition(ctx context.Context, m FoundryAgentV2ResourceModel) (client.AgentDefinitionV2, diag.Diagnostics) {
	var diags diag.Diagnostics
	def := client.AgentDefinitionV2{
		Kind:         m.Kind.ValueString(),
		Model:        m.Model.ValueString(),
		Instructions: m.Instructions.ValueString(),
	}
	tools, d := extractV2Tools(ctx, m.Tools)
	diags.Append(d...)
	def.Tools = tools

	if !m.StructuredInputsJSON.IsNull() && !m.StructuredInputsJSON.IsUnknown() && m.StructuredInputsJSON.ValueString() != "" {
		var structured map[string]interface{}
		if err := json.Unmarshal([]byte(m.StructuredInputsJSON.ValueString()), &structured); err != nil {
			diags.AddError("Invalid structured_inputs_json", err.Error())
		} else {
			def.StructuredInputs = structured
		}
	}

	// Hosted-agent / container_app wire-up. Only emit these fields when the
	// user set them; Foundry rejects the envelope for prompt agents.
	if !m.Image.IsNull() && !m.Image.IsUnknown() {
		def.Image = m.Image.ValueString()
	}
	if !m.Cpu.IsNull() && !m.Cpu.IsUnknown() {
		def.Cpu = m.Cpu.ValueString()
	}
	if !m.Memory.IsNull() && !m.Memory.IsUnknown() {
		def.Memory = m.Memory.ValueString()
	}
	if !m.ContainerProtocolVersions.IsNull() && !m.ContainerProtocolVersions.IsUnknown() {
		var pvs []protocolVersionModel
		diags.Append(m.ContainerProtocolVersions.ElementsAs(ctx, &pvs, false)...)
		records := make([]client.ProtocolVersionRecord, 0, len(pvs))
		for _, pv := range pvs {
			records = append(records, client.ProtocolVersionRecord{
				Protocol: pv.Protocol.ValueString(),
				Version:  pv.Version.ValueString(),
			})
		}
		def.ContainerProtocolVersions = records
	}
	if !m.EnvironmentVariables.IsNull() && !m.EnvironmentVariables.IsUnknown() {
		raw := make(map[string]types.String, len(m.EnvironmentVariables.Elements()))
		diags.Append(m.EnvironmentVariables.ElementsAs(ctx, &raw, false)...)
		env := make(map[string]string, len(raw))
		for k, v := range raw {
			env[k] = v.ValueString()
		}
		def.EnvironmentVariables = env
	}
	return def, diags
}

type protocolVersionModel struct {
	Protocol types.String `tfsdk:"protocol"`
	Version  types.String `tfsdk:"version"`
}

func responseToV2Model(_ context.Context, r *client.AgentResponseV2, m *FoundryAgentV2ResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(r.ID)
	m.Name = types.StringValue(r.Name)
	m.Version = types.StringValue(r.Versions.Latest.Version)
	m.CreatedAt = types.Int64Value(r.Versions.Latest.CreatedAt)
	m.Description = types.StringValue(r.Versions.Latest.Description)
	m.Kind = types.StringValue(r.Versions.Latest.Definition.Kind)
	m.Model = types.StringValue(r.Versions.Latest.Definition.Model)
	m.Instructions = types.StringValue(r.Versions.Latest.Definition.Instructions)

	if r.Versions.Latest.Metadata != nil {
		metaAttrs := make(map[string]attr.Value, len(r.Versions.Latest.Metadata))
		for k, v := range r.Versions.Latest.Metadata {
			metaAttrs[k] = types.StringValue(v)
		}
		metaMap, d := types.MapValue(types.StringType, metaAttrs)
		diags.Append(d...)
		m.Metadata = metaMap
	} else {
		m.Metadata = types.MapValueMust(types.StringType, map[string]attr.Value{})
	}

	if r.Versions.Latest.Definition.StructuredInputs != nil {
		if buf, err := json.Marshal(r.Versions.Latest.Definition.StructuredInputs); err == nil {
			m.StructuredInputsJSON = types.StringValue(string(buf))
		}
	}

	// Hosted-agent fields. Leave null for prompt/workflow kinds so state diffs
	// stay clean for users who never set them.
	def := r.Versions.Latest.Definition
	if def.Image != "" {
		m.Image = types.StringValue(def.Image)
	} else {
		m.Image = types.StringNull()
	}
	if def.Cpu != "" {
		m.Cpu = types.StringValue(def.Cpu)
	} else {
		m.Cpu = types.StringNull()
	}
	if def.Memory != "" {
		m.Memory = types.StringValue(def.Memory)
	} else {
		m.Memory = types.StringNull()
	}
	if len(def.ContainerProtocolVersions) > 0 {
		objs := make([]attr.Value, 0, len(def.ContainerProtocolVersions))
		for _, pv := range def.ContainerProtocolVersions {
			obj, d := types.ObjectValue(protocolVersionAttrTypes, map[string]attr.Value{
				"protocol": types.StringValue(pv.Protocol),
				"version":  types.StringValue(pv.Version),
			})
			diags.Append(d...)
			objs = append(objs, obj)
		}
		list, d := types.ListValue(types.ObjectType{AttrTypes: protocolVersionAttrTypes}, objs)
		diags.Append(d...)
		m.ContainerProtocolVersions = list
	} else {
		m.ContainerProtocolVersions = types.ListNull(types.ObjectType{AttrTypes: protocolVersionAttrTypes})
	}
	if len(def.EnvironmentVariables) > 0 {
		envAttrs := make(map[string]attr.Value, len(def.EnvironmentVariables))
		for k, v := range def.EnvironmentVariables {
			envAttrs[k] = types.StringValue(v)
		}
		envMap, d := types.MapValue(types.StringType, envAttrs)
		diags.Append(d...)
		m.EnvironmentVariables = envMap
	} else {
		m.EnvironmentVariables = types.MapNull(types.StringType)
	}

	if id := r.Versions.Latest.InstanceIdentity; id != nil {
		idObj, d := types.ObjectValue(instanceIdentityAttrTypes, map[string]attr.Value{
			"client_id":    types.StringValue(id.ClientID),
			"principal_id": types.StringValue(id.PrincipalID),
		})
		diags.Append(d...)
		m.InstanceIdentity = idObj
	} else {
		m.InstanceIdentity = types.ObjectNull(instanceIdentityAttrTypes)
	}

	toolObjects := make([]attr.Value, 0, len(r.Versions.Latest.Definition.Tools))
	for _, t := range r.Versions.Latest.Definition.Tools {
		toolMap, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		obj, d := wireToolToObject(toolMap)
		diags.Append(d...)
		toolObjects = append(toolObjects, obj)
	}
	toolList, d := types.ListValue(types.ObjectType{AttrTypes: toolAttrTypesV2}, toolObjects)
	diags.Append(d...)
	m.Tools = toolList
	return diags
}

// ─────────────────────────────────────────────────────────────────────────────
// Wire ↔ object conversion for individual tool variants
// ─────────────────────────────────────────────────────────────────────────────

func extractV2Tools(ctx context.Context, toolsList types.List) ([]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics
	if toolsList.IsNull() || toolsList.IsUnknown() {
		return nil, diags
	}

	var tools []toolModelV2
	diags.Append(toolsList.ElementsAs(ctx, &tools, false)...)
	if diags.HasError() {
		return nil, diags
	}

	result := make([]interface{}, 0, len(tools))
	for _, t := range tools {
		tt := t.Type.ValueString()
		switch tt {
		case "file_search":
			var vsIDs []string
			if !t.VectorStoreIDs.IsNull() && !t.VectorStoreIDs.IsUnknown() {
				diags.Append(t.VectorStoreIDs.ElementsAs(ctx, &vsIDs, false)...)
			}
			result = append(result, client.FileSearchToolV2{
				Type:           "file_search",
				VectorStoreIDs: vsIDs,
				MaxNumResults:  int(t.MaxNumResults.ValueInt64()),
			})
		case "code_interpreter":
			tool := client.CodeInterpreterToolV2{Type: "code_interpreter"}
			if !t.CodeInterpreter.IsNull() && !t.CodeInterpreter.IsUnknown() {
				attrs := t.CodeInterpreter.Attributes()
				container := &client.CodeInterpreterContainer{Type: "auto"}
				if v, ok := attrs["file_ids"].(types.List); ok && !v.IsNull() && !v.IsUnknown() {
					var ids []string
					diags.Append(v.ElementsAs(ctx, &ids, false)...)
					container.FileIDs = ids
				}
				tool.Container = container
			}
			result = append(result, tool)
		case "web_search":
			result = append(result, client.WebSearchToolV2{Type: "web_search"})
		case "bing_grounding":
			tool := client.BingGroundingToolV2{Type: "bing_grounding"}
			if !t.BingGrounding.IsNull() && !t.BingGrounding.IsUnknown() {
				attrs := t.BingGrounding.Attributes()
				if v, ok := attrs["connection_id"].(types.String); ok {
					tool.BingGrounding.ConnectionID = v.ValueString()
				}
			}
			result = append(result, tool)
		case "function":
			tool := client.FunctionToolV2{Type: "function"}
			if !t.Function.IsNull() && !t.Function.IsUnknown() {
				attrs := t.Function.Attributes()
				if v, ok := attrs["name"].(types.String); ok {
					tool.Name = v.ValueString()
				}
				if v, ok := attrs["description"].(types.String); ok {
					tool.Description = v.ValueString()
				}
				if v, ok := attrs["parameters_json"].(types.String); ok && !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
					var params map[string]interface{}
					if err := json.Unmarshal([]byte(v.ValueString()), &params); err != nil {
						diags.AddError("Invalid function.parameters_json", err.Error())
					} else {
						tool.Parameters = params
					}
				}
			}
			result = append(result, tool)
		case "openapi":
			tool := client.OpenAPIToolV2{Type: "openapi"}
			if !t.OpenAPI.IsNull() && !t.OpenAPI.IsUnknown() {
				attrs := t.OpenAPI.Attributes()
				if v, ok := attrs["name"].(types.String); ok {
					tool.OpenAPI.Name = v.ValueString()
				}
				if v, ok := attrs["description"].(types.String); ok {
					tool.OpenAPI.Description = v.ValueString()
				}
				if v, ok := attrs["spec_json"].(types.String); ok && !v.IsNull() && !v.IsUnknown() {
					var spec map[string]interface{}
					if err := json.Unmarshal([]byte(v.ValueString()), &spec); err != nil {
						diags.AddError("Invalid openapi.spec_json", err.Error())
					} else {
						tool.OpenAPI.Spec = spec
					}
				}
				authType := "anonymous"
				if v, ok := attrs["auth_type"].(types.String); ok && !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
					authType = v.ValueString()
				}
				tool.OpenAPI.Auth = client.OpenAPIAuth{Type: authType}
			}
			result = append(result, tool)
		case "mcp":
			tool := client.MCPToolV2{Type: "mcp"}
			if !t.MCP.IsNull() && !t.MCP.IsUnknown() {
				attrs := t.MCP.Attributes()
				if v, ok := attrs["server_label"].(types.String); ok {
					tool.ServerLabel = v.ValueString()
				}
				if v, ok := attrs["server_url"].(types.String); ok {
					tool.ServerURL = v.ValueString()
				}
				if v, ok := attrs["require_approval"].(types.String); ok {
					tool.RequireApproval = v.ValueString()
				}
				if v, ok := attrs["project_connection_id"].(types.String); ok {
					tool.ProjectConnectionID = v.ValueString()
				}
			}
			result = append(result, tool)
		case "azure_ai_search":
			tool := client.AzureAISearchToolV2{Type: "azure_ai_search"}
			if !t.AzureAISearch.IsNull() && !t.AzureAISearch.IsUnknown() {
				attrs := t.AzureAISearch.Attributes()
				if v, ok := attrs["indexes"].(types.List); ok && !v.IsNull() && !v.IsUnknown() {
					for _, elem := range v.Elements() {
						idxObj, ok := elem.(types.Object)
						if !ok {
							continue
						}
						a := idxObj.Attributes()
						idx := client.AzureAISearchIndex{}
						if vv, ok := a["project_connection_id"].(types.String); ok {
							idx.ProjectConnectionID = vv.ValueString()
						}
						if vv, ok := a["index_name"].(types.String); ok {
							idx.IndexName = vv.ValueString()
						}
						if vv, ok := a["query_type"].(types.String); ok {
							idx.QueryType = vv.ValueString()
						}
						if vv, ok := a["top_k"].(types.Int64); ok {
							idx.TopK = int(vv.ValueInt64())
						}
						tool.AzureAISearch.Indexes = append(tool.AzureAISearch.Indexes, idx)
					}
				}
			}
			result = append(result, tool)
		case "memory_search":
			tool := client.MemorySearchToolV2{Type: "memory_search_preview"}
			if !t.MemorySearch.IsNull() && !t.MemorySearch.IsUnknown() {
				attrs := t.MemorySearch.Attributes()
				if v, ok := attrs["memory_store_name"].(types.String); ok {
					tool.MemoryStoreName = v.ValueString()
				}
				if v, ok := attrs["scope"].(types.String); ok && !v.IsNull() && !v.IsUnknown() {
					tool.Scope = v.ValueString()
				}
				if v, ok := attrs["update_delay"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
					tool.UpdateDelay = int(v.ValueInt64())
				}
			}
			result = append(result, tool)
		default:
			diags.AddError("Unsupported tool type", fmt.Sprintf("tool type %q is not supported", tt))
		}
	}
	return result, diags
}

// wireToolToObject builds a tool list element with all variant slots null
// except the one matching the wire payload.
func wireToolToObject(toolMap map[string]interface{}) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	tt, _ := toolMap["type"].(string)

	values := map[string]attr.Value{
		"type":             types.StringValue(tt),
		"vector_store_ids": types.ListNull(types.StringType),
		"max_num_results":  types.Int64Null(),
		"code_interpreter": types.ObjectNull(codeInterpreterAttrTypes),
		"function":         types.ObjectNull(functionAttrTypes),
		"openapi":          types.ObjectNull(openapiAttrTypes),
		"mcp":              types.ObjectNull(mcpAttrTypes),
		"azure_ai_search":  types.ObjectNull(azureAISearchAttrTypes),
		"bing_grounding":   types.ObjectNull(bingGroundingAttrTypes),
		"memory_search":    types.ObjectNull(memorySearchAttrTypes),
	}

	// Foundry emits the memory-search tool with type="memory_search_preview"
	// during preview; fold it back onto the stable "memory_search" schema key.
	if tt == "memory_search_preview" {
		tt = "memory_search"
		values["type"] = types.StringValue("memory_search")
	}

	switch tt {
	case "file_search":
		if vsRaw, ok := toolMap["vector_store_ids"].([]interface{}); ok {
			vals := make([]attr.Value, len(vsRaw))
			for i, v := range vsRaw {
				vals[i] = types.StringValue(fmt.Sprintf("%v", v))
			}
			lst, d := types.ListValue(types.StringType, vals)
			diags.Append(d...)
			values["vector_store_ids"] = lst
		}
		if mr, ok := toolMap["max_num_results"].(float64); ok {
			values["max_num_results"] = types.Int64Value(int64(mr))
		}
	case "code_interpreter":
		if c, ok := toolMap["container"].(map[string]interface{}); ok {
			fileIDsRaw, _ := c["file_ids"].([]interface{})
			vals := make([]attr.Value, len(fileIDsRaw))
			for i, v := range fileIDsRaw {
				vals[i] = types.StringValue(fmt.Sprintf("%v", v))
			}
			lst, d := types.ListValue(types.StringType, vals)
			diags.Append(d...)
			obj, d := types.ObjectValue(codeInterpreterAttrTypes, map[string]attr.Value{"file_ids": lst})
			diags.Append(d...)
			values["code_interpreter"] = obj
		}
	case "bing_grounding":
		conn := ""
		if bg, ok := toolMap["bing_grounding"].(map[string]interface{}); ok {
			if cid, ok := bg["connection_id"].(string); ok {
				conn = cid
			}
		}
		obj, d := types.ObjectValue(bingGroundingAttrTypes, map[string]attr.Value{
			"connection_id": types.StringValue(conn),
		})
		diags.Append(d...)
		values["bing_grounding"] = obj
	case "function":
		name, _ := toolMap["name"].(string)
		desc, _ := toolMap["description"].(string)
		paramsJSON := ""
		if params, ok := toolMap["parameters"].(map[string]interface{}); ok {
			if buf, err := json.Marshal(params); err == nil {
				paramsJSON = string(buf)
			}
		}
		obj, d := types.ObjectValue(functionAttrTypes, map[string]attr.Value{
			"name":            types.StringValue(name),
			"description":     types.StringValue(desc),
			"parameters_json": types.StringValue(paramsJSON),
		})
		diags.Append(d...)
		values["function"] = obj
	case "openapi":
		oa, _ := toolMap["openapi"].(map[string]interface{})
		name, _ := oa["name"].(string)
		desc, _ := oa["description"].(string)
		specJSON := ""
		if spec, ok := oa["spec"].(map[string]interface{}); ok {
			if buf, err := json.Marshal(spec); err == nil {
				specJSON = string(buf)
			}
		}
		authType := ""
		if auth, ok := oa["auth"].(map[string]interface{}); ok {
			if at, ok := auth["type"].(string); ok {
				authType = at
			}
		}
		obj, d := types.ObjectValue(openapiAttrTypes, map[string]attr.Value{
			"name":        types.StringValue(name),
			"description": types.StringValue(desc),
			"spec_json":   types.StringValue(specJSON),
			"auth_type":   types.StringValue(authType),
		})
		diags.Append(d...)
		values["openapi"] = obj
	case "mcp":
		serverLabel, _ := toolMap["server_label"].(string)
		serverURL, _ := toolMap["server_url"].(string)
		requireApproval, _ := toolMap["require_approval"].(string)
		connID, _ := toolMap["project_connection_id"].(string)
		obj, d := types.ObjectValue(mcpAttrTypes, map[string]attr.Value{
			"server_label":          types.StringValue(serverLabel),
			"server_url":            types.StringValue(serverURL),
			"require_approval":      types.StringValue(requireApproval),
			"project_connection_id": types.StringValue(connID),
		})
		diags.Append(d...)
		values["mcp"] = obj
	case "azure_ai_search":
		ais, _ := toolMap["azure_ai_search"].(map[string]interface{})
		idxRaw, _ := ais["indexes"].([]interface{})
		idxObjs := make([]attr.Value, 0, len(idxRaw))
		for _, ir := range idxRaw {
			im, ok := ir.(map[string]interface{})
			if !ok {
				continue
			}
			conn, _ := im["project_connection_id"].(string)
			idxName, _ := im["index_name"].(string)
			qt, _ := im["query_type"].(string)
			topK := int64(0)
			if tk, ok := im["top_k"].(float64); ok {
				topK = int64(tk)
			}
			obj, d := types.ObjectValue(azureAISearchIndexAttrTypes, map[string]attr.Value{
				"project_connection_id": types.StringValue(conn),
				"index_name":            types.StringValue(idxName),
				"query_type":            types.StringValue(qt),
				"top_k":                 types.Int64Value(topK),
			})
			diags.Append(d...)
			idxObjs = append(idxObjs, obj)
		}
		idxList, d := types.ListValue(types.ObjectType{AttrTypes: azureAISearchIndexAttrTypes}, idxObjs)
		diags.Append(d...)
		obj, d := types.ObjectValue(azureAISearchAttrTypes, map[string]attr.Value{"indexes": idxList})
		diags.Append(d...)
		values["azure_ai_search"] = obj
	case "memory_search":
		storeName, _ := toolMap["memory_store_name"].(string)
		scope, _ := toolMap["scope"].(string)
		delay := int64(0)
		if d, ok := toolMap["update_delay"].(float64); ok {
			delay = int64(d)
		}
		obj, d := types.ObjectValue(memorySearchAttrTypes, map[string]attr.Value{
			"memory_store_name": types.StringValue(storeName),
			"scope":             types.StringValue(scope),
			"update_delay":      types.Int64Value(delay),
		})
		diags.Append(d...)
		values["memory_search"] = obj
	}

	obj, d := types.ObjectValue(toolAttrTypesV2, values)
	diags.Append(d...)
	return obj, diags
}
