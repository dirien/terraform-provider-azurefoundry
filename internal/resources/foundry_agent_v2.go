// Copyright (c) Engin Diri
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

var (
	_ resource.Resource                = &FoundryAgentV2Resource{}
	_ resource.ResourceWithImportState = &FoundryAgentV2Resource{}
)

// User-facing tool-type identifiers. The wire spelling for memory search is
// "memory_search_preview" while the feature is in preview; we keep the
// stable user-facing spelling "memory_search" and translate at the boundary.
const (
	toolTypeMemorySearch        = "memory_search"
	toolTypeMemorySearchPreview = "memory_search_preview"
)

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
	CPU                       types.String `tfsdk:"cpu"`
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
		MarkdownDescription: "Manages an Azure AI Foundry Agent (v2 API).\n\n" +
			"The v2 API supports prompt agents (LLM + tools) and hosted agents " +
			"(`kind = \"hosted\"` or `\"container_app\"`) where you ship a container image " +
			"that speaks the Foundry agent protocols. Most fields are common to both kinds; " +
			"the hosted-only fields (`image`, `cpu`, `memory`, `container_protocol_versions`, " +
			"`environment_variables`) are ignored when `kind = \"prompt\"`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Foundry-assigned agent ID. Stable for the lifetime of the agent.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "User-supplied agent name. Unique within the Foundry project. " +
					"Changing this forces replacement.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Human-readable description, surfaced in the Foundry portal.",
				Optional:            true,
				Computed:            true,
			},
			"created_at": schema.Int64Attribute{
				MarkdownDescription: "Unix timestamp (seconds) when the latest version of the agent was created.",
				Computed:            true,
			},
			"version": schema.StringAttribute{
				MarkdownDescription: "Foundry-assigned version identifier for the latest definition. " +
					"Increments on each Update.",
				Computed: true,
			},
			"metadata": schema.MapAttribute{
				MarkdownDescription: "Arbitrary key/value labels stored alongside the agent. Up to 16 entries.",
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
			},
			"kind": schema.StringAttribute{
				MarkdownDescription: "Agent kind. One of `prompt` (LLM + tools, no container), " +
					"`hosted` / `container_app` (you ship a container image), or `workflow` " +
					"(experimental). The hosted-only fields are required when `kind` is " +
					"`hosted` or `container_app`, ignored otherwise.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf("prompt", "hosted", "container_app", "workflow"),
				},
			},
			"model": schema.StringAttribute{
				MarkdownDescription: "Model deployment name (e.g. `gpt-4o-mini`). For prompt agents this is the " +
					"underlying LLM; for hosted agents it's the runtime model your container talks to via the " +
					"Foundry-managed identity.",
				Required: true,
			},
			"instructions": schema.StringAttribute{
				MarkdownDescription: "System prompt for the agent. Ignored for hosted agents — the container " +
					"defines its own behavior.",
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
					"client_id": schema.StringAttribute{
						MarkdownDescription: "Application (client) ID of the agent's managed identity.",
						Computed:            true,
					},
					"principal_id": schema.StringAttribute{
						MarkdownDescription: "Object (principal) ID of the agent's managed identity. " +
							"Use this in role assignments.",
						Computed: true,
					},
				},
			},
			"container_protocol_versions": schema.ListNestedAttribute{
				MarkdownDescription: "Protocols the container speaks. Required for `container_app`/`hosted` kinds. Today the valid protocols are `responses` (Azure OpenAI Responses API) and `a2a` (Agent-to-Agent).",
				Optional:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"protocol": schema.StringAttribute{
							MarkdownDescription: "`responses` or `a2a`.",
							Required:            true,
						},
						"version": schema.StringAttribute{
							MarkdownDescription: "Protocol version, e.g. `v1`.",
							Required:            true,
						},
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
				MarkdownDescription: "Tools enabled for the agent. Order is preserved on the wire. " +
					"Set the variant block (`code_interpreter`, `function`, `mcp`, ...) matching the `type`; " +
					"the others are ignored.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							MarkdownDescription: "Tool type. One of `file_search`, `code_interpreter`, `web_search`, " +
								"`bing_grounding`, `function`, `openapi`, `mcp`, `azure_ai_search`, `memory_search`.",
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
							MarkdownDescription: "Vector store IDs to search. Used when `type = \"file_search\"`.",
							Optional:            true,
							ElementType:         types.StringType,
						},
						"max_num_results": schema.Int64Attribute{
							MarkdownDescription: "Maximum search hits returned per query. Used when " +
								"`type = \"file_search\"`. Defaults to the Foundry server-side default.",
							Optional: true,
						},
						"code_interpreter": schema.SingleNestedAttribute{
							MarkdownDescription: "Sandboxed Python execution. Used when `type = \"code_interpreter\"`.",
							Optional:            true,
							Attributes: map[string]schema.Attribute{
								"file_ids": schema.ListAttribute{
									MarkdownDescription: "File IDs available to the code interpreter sandbox.",
									Optional:            true,
									ElementType:         types.StringType,
								},
							},
						},
						"function": schema.SingleNestedAttribute{
							MarkdownDescription: "OpenAI-style function calling. Used when `type = \"function\"`. " +
								"`parameters_json` is a JSON Schema describing the function's arguments.",
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{
									MarkdownDescription: "Function name the model will call.",
									Required:            true,
								},
								"description": schema.StringAttribute{
									MarkdownDescription: "Human-readable description shown to the model.",
									Optional:            true,
								},
								"parameters_json": schema.StringAttribute{
									MarkdownDescription: "JSON Schema (as a string) for the function's parameters. " +
										"Use `jsonencode({...})` in HCL.",
									Optional: true,
								},
							},
						},
						"openapi": schema.SingleNestedAttribute{
							MarkdownDescription: "OpenAPI spec inlined as a tool. Used when `type = \"openapi\"`.",
							Optional:            true,
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{
									MarkdownDescription: "Tool name surfaced to the model.",
									Required:            true,
								},
								"description": schema.StringAttribute{
									MarkdownDescription: "Human-readable description shown to the model.",
									Optional:            true,
								},
								"spec_json": schema.StringAttribute{
									MarkdownDescription: "OpenAPI 3.x spec serialized as a JSON string. " +
										"Use `jsonencode({...})` or `file(\"spec.json\")` in HCL.",
									Required: true,
								},
								"auth_type": schema.StringAttribute{
									MarkdownDescription: "`anonymous` (default) or `connection`. When `connection`, " +
										"Foundry uses the project connection bound to the API host.",
									Optional: true,
								},
							},
						},
						"mcp": schema.SingleNestedAttribute{
							MarkdownDescription: "Model Context Protocol server. Used when `type = \"mcp\"`.",
							Optional:            true,
							Attributes: map[string]schema.Attribute{
								"server_label": schema.StringAttribute{
									MarkdownDescription: "Display label for the MCP server in tool-call traces.",
									Required:            true,
								},
								"server_url": schema.StringAttribute{
									MarkdownDescription: "URL of the MCP server. Must be reachable from Foundry's egress.",
									Required:            true,
								},
								"require_approval": schema.StringAttribute{
									MarkdownDescription: "`always`, `never`, or omitted (Foundry default). Controls whether " +
										"the user must approve tool invocations before they run.",
									Optional: true,
								},
								"project_connection_id": schema.StringAttribute{
									MarkdownDescription: "Project connection ID used to authenticate to the MCP server.",
									Optional:            true,
								},
							},
						},
						"azure_ai_search": schema.SingleNestedAttribute{
							MarkdownDescription: "Azure AI Search retrieval. Used when `type = \"azure_ai_search\"`. " +
								"One or more `indexes` are queried per request.",
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"indexes": schema.ListNestedAttribute{
									MarkdownDescription: "Indexes to query.",
									Required:            true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"project_connection_id": schema.StringAttribute{
												MarkdownDescription: "Project connection ID for the Azure AI Search account.",
												Required:            true,
											},
											"index_name": schema.StringAttribute{
												MarkdownDescription: "Name of the index within the search account.",
												Required:            true,
											},
											"query_type": schema.StringAttribute{
												MarkdownDescription: "`simple`, `semantic`, `vector`, `vector_simple_hybrid`, or " +
													"`vector_semantic_hybrid`. Defaults to Foundry's preference.",
												Optional: true,
											},
											"top_k": schema.Int64Attribute{
												MarkdownDescription: "Maximum hits returned per query. Defaults to the " +
													"server-side default.",
												Optional: true,
											},
										},
									},
								},
							},
						},
						"bing_grounding": schema.SingleNestedAttribute{
							MarkdownDescription: "Bing Search v7 grounding via a project connection. " +
								"Used when `type = \"bing_grounding\"`. For the managed Foundry-hosted variant " +
								"that needs no connection, use `type = \"web_search\"`.",
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"connection_id": schema.StringAttribute{
									MarkdownDescription: "Project connection ID bound to the Bing Search resource.",
									Required:            true,
								},
							},
						},
						"memory_search": schema.SingleNestedAttribute{
							MarkdownDescription: "Attach a Foundry Memory store (preview). Used when " +
								"`type = \"memory_search\"`. The wire spelling is `memory_search_preview` while " +
								"the feature is in preview; this provider accepts the shorter `memory_search` and " +
								"translates at the boundary so consumer HCL stays stable across the GA cut-over.",
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"memory_store_name": schema.StringAttribute{
									MarkdownDescription: "Name of the `azurefoundry_memory_store_v2` to attach.",
									Required:            true,
								},
								"scope": schema.StringAttribute{
									MarkdownDescription: "Memory scope expression. Use `\"{{$userId}}\"` to resolve from " +
										"the `x-memory-user-id` header or the caller's Entra identity.",
									Optional: true,
								},
								"update_delay": schema.Int64Attribute{
									MarkdownDescription: "Seconds of inactivity before extracted memories are written " +
										"back to the store. Defaults to 300.",
									Optional: true,
								},
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

	tflog.Debug(ctx, "Creating Foundry agent", map[string]any{"name": apiReq.Name, "model": apiReq.Definition.Model})

	// Block until the project's data plane is reachable (project routing +
	// RBAC propagation). First Create per session pays the cost; the rest
	// short-circuit via a cached flag.
	if err := r.client.WaitForProjectReady(ctx, 30*time.Minute); err != nil {
		resp.Diagnostics.AddError("Foundry project not reachable", err.Error())
		return
	}

	resp.Diagnostics.Append(r.preflightAgentMustNotExist(ctx, apiReq.Name)...)
	if resp.Diagnostics.HasError() {
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

	resp.Diagnostics.Append(r.warmupIfRequested(ctx, plan, apiReq.Name)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// preflightAgentMustNotExist shrinks the orphan-creation race. If an agent
// with this name already exists in the data plane, fail with the import hint
// before we POST — that converts an inscrutable 409 from an earlier run's
// orphan into an actionable error here. The remaining race window between
// this GET and the POST is one HTTP roundtrip; a true concurrent create from
// another caller still surfaces the same import-hint error via isConflict.
func (r *FoundryAgentV2Resource) preflightAgentMustNotExist(ctx context.Context, name string) diag.Diagnostics {
	var diags diag.Diagnostics
	existing, err := r.client.GetAgentV2(ctx, name)
	switch {
	case err == nil && existing != nil:
		summary, detail := alreadyExistsError(
			"agent", name,
			"azurefoundry_agent_v2", "azurefoundry:index:AgentV2",
		)
		diags.AddError(summary, detail)
	case err != nil && !isNotFound(err):
		diags.AddError("Pre-flight existence check failed", err.Error())
	}
	return diags
}

// warmupIfRequested polls the agent's Responses endpoint until it stops
// returning HTTP 424 (session_not_ready). Opt-in via plan.Warmup and only
// meaningful for hosted agents — prompt agents are reachable as soon as
// Create returns (no per-session sandbox to cold-start).
func (r *FoundryAgentV2Resource) warmupIfRequested(ctx context.Context, plan FoundryAgentV2ResourceModel, name string) diag.Diagnostics {
	var diags diag.Diagnostics
	wantWarmup := !plan.Warmup.IsNull() && !plan.Warmup.IsUnknown() && plan.Warmup.ValueBool()
	isHosted := plan.Kind.ValueString() == "hosted" || plan.Kind.ValueString() == "container_app"
	if !wantWarmup || !isHosted {
		return diags
	}

	timeout, d := parseWarmupTimeout(plan.WarmupTimeout)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}

	tflog.Debug(ctx, "Warming up Foundry agent", map[string]any{"name": name, "timeout": timeout.String()})
	if err := r.client.WaitForAgentV2Ready(ctx, name, timeout, 5*time.Second); err != nil {
		diags.AddError("Agent warmup failed", err.Error())
	}
	return diags
}

func parseWarmupTimeout(v types.String) (time.Duration, diag.Diagnostics) {
	const defaultTimeout = 5 * time.Minute
	if v.IsNull() || v.IsUnknown() {
		return defaultTimeout, nil
	}
	d, err := time.ParseDuration(v.ValueString())
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError(
			"Invalid warmup_timeout",
			fmt.Sprintf("warmup_timeout %q is not a valid Go duration: %s", v.ValueString(), err.Error()),
		)
		return 0, diags
	}
	if d <= 0 {
		return defaultTimeout, nil
	}
	return d, nil
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

	tflog.Debug(ctx, "Updating Foundry agent", map[string]any{"id": state.Name.ValueString()})

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

	tflog.Debug(ctx, "Deleting Foundry agent", map[string]any{"id": state.Name.ValueString()})

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

	if structured, d := decodeStructuredInputs(m.StructuredInputsJSON); structured != nil {
		def.StructuredInputs = structured
	} else {
		diags.Append(d...)
	}

	diags.Append(applyHostedAgentFields(ctx, m, &def)...)
	return def, diags
}

func decodeStructuredInputs(v types.String) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil, diags
	}
	var structured map[string]any
	if err := json.Unmarshal([]byte(v.ValueString()), &structured); err != nil {
		diags.AddError("Invalid structured_inputs_json", err.Error())
		return nil, diags
	}
	return structured, diags
}

// applyHostedAgentFields populates the hosted-only fields on def. Only emits
// values the user actually set — Foundry rejects the hosted envelope for
// prompt agents.
func applyHostedAgentFields(ctx context.Context, m FoundryAgentV2ResourceModel, def *client.AgentDefinitionV2) diag.Diagnostics {
	var diags diag.Diagnostics
	def.Image = stringOrEmpty(m.Image)
	def.CPU = stringOrEmpty(m.CPU)
	def.Memory = stringOrEmpty(m.Memory)

	pvs, d := extractProtocolVersions(ctx, m.ContainerProtocolVersions)
	diags.Append(d...)
	def.ContainerProtocolVersions = pvs

	env, d := extractMetadata(ctx, m.EnvironmentVariables)
	diags.Append(d...)
	def.EnvironmentVariables = env

	return diags
}

func stringOrEmpty(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func extractProtocolVersions(ctx context.Context, l types.List) ([]client.ProtocolVersionRecord, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var pvs []protocolVersionModel
	diags := l.ElementsAs(ctx, &pvs, false)
	out := make([]client.ProtocolVersionRecord, 0, len(pvs))
	for _, pv := range pvs {
		out = append(out, client.ProtocolVersionRecord{
			Protocol: pv.Protocol.ValueString(),
			Version:  pv.Version.ValueString(),
		})
	}
	return out, diags
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
	if def.CPU != "" {
		m.CPU = types.StringValue(def.CPU)
	} else {
		m.CPU = types.StringNull()
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
		toolMap, ok := t.(map[string]any)
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

// toolExtractor builds the wire-format payload for a single tool entry from
// its config-side Plugin Framework model.
type toolExtractor func(ctx context.Context, t *toolModelV2) (any, diag.Diagnostics)

// toolExtractors dispatches by the user-facing `type` value. Each entry is
// responsible for *just* its tool's branch — keeping any single extractor
// well below the gocyclo budget. Adding a new tool type is one map entry +
// one function below, no surgery on extractV2Tools itself.
var toolExtractors = map[string]toolExtractor{
	"file_search":        extractFileSearchTool,
	"code_interpreter":   extractCodeInterpreterTool,
	"web_search":         extractWebSearchTool,
	"bing_grounding":     extractBingGroundingTool,
	"function":           extractFunctionTool,
	"openapi":            extractOpenAPITool,
	"mcp":                extractMCPTool,
	"azure_ai_search":    extractAzureAISearchTool,
	toolTypeMemorySearch: extractMemorySearchTool,
}

func extractV2Tools(ctx context.Context, toolsList types.List) ([]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	if toolsList.IsNull() || toolsList.IsUnknown() {
		return nil, diags
	}

	var tools []toolModelV2
	diags.Append(toolsList.ElementsAs(ctx, &tools, false)...)
	if diags.HasError() {
		return nil, diags
	}

	result := make([]any, 0, len(tools))
	for i := range tools {
		t := &tools[i]
		tt := t.Type.ValueString()
		extractor, ok := toolExtractors[tt]
		if !ok {
			diags.AddError("Unsupported tool type", fmt.Sprintf("tool type %q is not supported", tt))
			continue
		}
		payload, d := extractor(ctx, t)
		diags.Append(d...)
		result = append(result, payload)
	}
	return result, diags
}

func extractFileSearchTool(ctx context.Context, t *toolModelV2) (any, diag.Diagnostics) {
	vsIDs, diags := extractStringList(ctx, t.VectorStoreIDs)
	return client.FileSearchToolV2{
		Type:           "file_search",
		VectorStoreIDs: vsIDs,
		MaxNumResults:  int(t.MaxNumResults.ValueInt64()),
	}, diags
}

func extractCodeInterpreterTool(ctx context.Context, t *toolModelV2) (any, diag.Diagnostics) {
	tool := client.CodeInterpreterToolV2{Type: "code_interpreter"}
	if t.CodeInterpreter.IsNull() || t.CodeInterpreter.IsUnknown() {
		return tool, nil
	}
	attrs := t.CodeInterpreter.Attributes()
	container := &client.CodeInterpreterContainer{Type: "auto"}
	var diags diag.Diagnostics
	if v, ok := attrs["file_ids"].(types.List); ok {
		ids, d := extractStringList(ctx, v)
		diags.Append(d...)
		container.FileIDs = ids
	}
	tool.Container = container
	return tool, diags
}

func extractWebSearchTool(_ context.Context, _ *toolModelV2) (any, diag.Diagnostics) {
	return client.WebSearchToolV2{Type: "web_search"}, nil
}

func extractBingGroundingTool(_ context.Context, t *toolModelV2) (any, diag.Diagnostics) {
	tool := client.BingGroundingToolV2{Type: "bing_grounding"}
	if t.BingGrounding.IsNull() || t.BingGrounding.IsUnknown() {
		return tool, nil
	}
	attrs := t.BingGrounding.Attributes()
	tool.BingGrounding.ConnectionID = stringAttr(attrs, "connection_id")
	return tool, nil
}

func extractFunctionTool(_ context.Context, t *toolModelV2) (any, diag.Diagnostics) {
	tool := client.FunctionToolV2{Type: "function"}
	if t.Function.IsNull() || t.Function.IsUnknown() {
		return tool, nil
	}
	attrs := t.Function.Attributes()
	tool.Name = stringAttr(attrs, "name")
	tool.Description = stringAttr(attrs, "description")
	params, diags := decodeJSONStringAttr(attrs, "parameters_json", "function.parameters_json")
	if params != nil {
		tool.Parameters = params
	}
	return tool, diags
}

func extractOpenAPITool(_ context.Context, t *toolModelV2) (any, diag.Diagnostics) {
	tool := client.OpenAPIToolV2{Type: "openapi"}
	if t.OpenAPI.IsNull() || t.OpenAPI.IsUnknown() {
		return tool, nil
	}
	attrs := t.OpenAPI.Attributes()
	tool.OpenAPI.Name = stringAttr(attrs, "name")
	tool.OpenAPI.Description = stringAttr(attrs, "description")

	spec, diags := decodeJSONStringAttr(attrs, "spec_json", "openapi.spec_json")
	if spec != nil {
		tool.OpenAPI.Spec = spec
	}

	authType := stringAttr(attrs, "auth_type")
	if authType == "" {
		authType = "anonymous"
	}
	tool.OpenAPI.Auth = client.OpenAPIAuth{Type: authType}
	return tool, diags
}

func extractMCPTool(_ context.Context, t *toolModelV2) (any, diag.Diagnostics) {
	tool := client.MCPToolV2{Type: "mcp"}
	if t.MCP.IsNull() || t.MCP.IsUnknown() {
		return tool, nil
	}
	attrs := t.MCP.Attributes()
	tool.ServerLabel = stringAttr(attrs, "server_label")
	tool.ServerURL = stringAttr(attrs, "server_url")
	tool.RequireApproval = stringAttr(attrs, "require_approval")
	tool.ProjectConnectionID = stringAttr(attrs, "project_connection_id")
	return tool, nil
}

func extractAzureAISearchTool(_ context.Context, t *toolModelV2) (any, diag.Diagnostics) {
	tool := client.AzureAISearchToolV2{Type: "azure_ai_search"}
	if t.AzureAISearch.IsNull() || t.AzureAISearch.IsUnknown() {
		return tool, nil
	}
	attrs := t.AzureAISearch.Attributes()
	v, ok := attrs["indexes"].(types.List)
	if !ok || v.IsNull() || v.IsUnknown() {
		return tool, nil
	}
	for _, elem := range v.Elements() {
		idxObj, ok := elem.(types.Object)
		if !ok {
			continue
		}
		tool.AzureAISearch.Indexes = append(tool.AzureAISearch.Indexes, buildAzureAISearchIndex(idxObj.Attributes()))
	}
	return tool, nil
}

func buildAzureAISearchIndex(a map[string]attr.Value) client.AzureAISearchIndex {
	idx := client.AzureAISearchIndex{
		ProjectConnectionID: stringAttr(a, "project_connection_id"),
		IndexName:           stringAttr(a, "index_name"),
		QueryType:           stringAttr(a, "query_type"),
	}
	if v, ok := a["top_k"].(types.Int64); ok {
		idx.TopK = int(v.ValueInt64())
	}
	return idx
}

func extractMemorySearchTool(_ context.Context, t *toolModelV2) (any, diag.Diagnostics) {
	tool := client.MemorySearchToolV2{Type: toolTypeMemorySearchPreview}
	if t.MemorySearch.IsNull() || t.MemorySearch.IsUnknown() {
		return tool, nil
	}
	attrs := t.MemorySearch.Attributes()
	tool.MemoryStoreName = stringAttr(attrs, "memory_store_name")
	tool.Scope = stringAttr(attrs, "scope")
	if v, ok := attrs["update_delay"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		tool.UpdateDelay = int(v.ValueInt64())
	}
	return tool, nil
}

// stringAttr extracts a string attribute, returning "" when the key is
// missing, the type doesn't match, or the value is null/unknown. Used by
// the per-tool extractors to keep them branch-light.
func stringAttr(attrs map[string]attr.Value, key string) string {
	v, ok := attrs[key].(types.String)
	if !ok || v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

// decodeJSONStringAttr unmarshals a string attribute holding JSON. Returns
// (nil, nil) when the key is absent or empty; (nil, diag) on malformed JSON.
// label is the user-facing attribute name used in error messages.
func decodeJSONStringAttr(attrs map[string]attr.Value, key, label string) (map[string]any, diag.Diagnostics) {
	v, ok := attrs[key].(types.String)
	if !ok || v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(v.ValueString()), &out); err != nil {
		var diags diag.Diagnostics
		diags.AddError("Invalid "+label, err.Error())
		return nil, diags
	}
	return out, nil
}

// toolWirer fills the per-tool variant slot in `values`. Returns any
// diagnostics from constructing nested types.Object/types.List values.
type toolWirer func(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics

var toolWirers = map[string]toolWirer{
	"file_search":        wireFileSearchTool,
	"code_interpreter":   wireCodeInterpreterTool,
	"bing_grounding":     wireBingGroundingTool,
	"function":           wireFunctionTool,
	"openapi":            wireOpenAPITool,
	"mcp":                wireMCPTool,
	"azure_ai_search":    wireAzureAISearchTool,
	toolTypeMemorySearch: wireMemorySearchTool,
}

// wireToolToObject builds a tool list element with all variant slots null
// except the one matching the wire payload.
func wireToolToObject(toolMap map[string]any) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	tt, _ := toolMap["type"].(string)

	values := nullToolVariantSlots(tt)

	// Foundry emits the memory-search tool with type="memory_search_preview"
	// during preview; fold it back onto the stable "memory_search" schema key.
	if tt == toolTypeMemorySearchPreview {
		tt = toolTypeMemorySearch
		values["type"] = types.StringValue(toolTypeMemorySearch)
	}

	if wirer, ok := toolWirers[tt]; ok {
		diags.Append(wirer(toolMap, values)...)
	}

	obj, d := types.ObjectValue(toolAttrTypesV2, values)
	diags.Append(d...)
	return obj, diags
}

func nullToolVariantSlots(tt string) map[string]attr.Value {
	return map[string]attr.Value{
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
}

func wireFileSearchTool(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics {
	var diags diag.Diagnostics
	if vsRaw, ok := toolMap["vector_store_ids"].([]any); ok {
		lst, d := stringListFromAny(vsRaw)
		diags.Append(d...)
		values["vector_store_ids"] = lst
	}
	if mr, ok := toolMap["max_num_results"].(float64); ok {
		values["max_num_results"] = types.Int64Value(int64(mr))
	}
	return diags
}

func wireCodeInterpreterTool(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics {
	c, ok := toolMap["container"].(map[string]any)
	if !ok {
		return nil
	}
	var diags diag.Diagnostics
	fileIDsRaw, _ := c["file_ids"].([]any)
	lst, d := stringListFromAny(fileIDsRaw)
	diags.Append(d...)
	obj, d := types.ObjectValue(codeInterpreterAttrTypes, map[string]attr.Value{"file_ids": lst})
	diags.Append(d...)
	values["code_interpreter"] = obj
	return diags
}

func wireBingGroundingTool(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics {
	conn := stringFromMap(asMap(toolMap["bing_grounding"]), "connection_id")
	obj, diags := types.ObjectValue(bingGroundingAttrTypes, map[string]attr.Value{
		"connection_id": types.StringValue(conn),
	})
	values["bing_grounding"] = obj
	return diags
}

func wireFunctionTool(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics {
	obj, diags := types.ObjectValue(functionAttrTypes, map[string]attr.Value{
		"name":            types.StringValue(stringFromMap(toolMap, "name")),
		"description":     types.StringValue(stringFromMap(toolMap, "description")),
		"parameters_json": types.StringValue(jsonStringFromMap(toolMap, "parameters")),
	})
	values["function"] = obj
	return diags
}

func wireOpenAPITool(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics {
	oa := asMap(toolMap["openapi"])
	authType := stringFromMap(asMap(oa["auth"]), "type")
	obj, diags := types.ObjectValue(openapiAttrTypes, map[string]attr.Value{
		"name":        types.StringValue(stringFromMap(oa, "name")),
		"description": types.StringValue(stringFromMap(oa, "description")),
		"spec_json":   types.StringValue(jsonStringFromMap(oa, "spec")),
		"auth_type":   types.StringValue(authType),
	})
	values["openapi"] = obj
	return diags
}

func wireMCPTool(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics {
	obj, diags := types.ObjectValue(mcpAttrTypes, map[string]attr.Value{
		"server_label":          types.StringValue(stringFromMap(toolMap, "server_label")),
		"server_url":            types.StringValue(stringFromMap(toolMap, "server_url")),
		"require_approval":      types.StringValue(stringFromMap(toolMap, "require_approval")),
		"project_connection_id": types.StringValue(stringFromMap(toolMap, "project_connection_id")),
	})
	values["mcp"] = obj
	return diags
}

func wireAzureAISearchTool(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics {
	var diags diag.Diagnostics
	ais := asMap(toolMap["azure_ai_search"])
	idxRaw, _ := ais["indexes"].([]any)

	idxObjs := make([]attr.Value, 0, len(idxRaw))
	for _, ir := range idxRaw {
		im, ok := ir.(map[string]any)
		if !ok {
			continue
		}
		obj, d := wireAzureAISearchIndex(im)
		diags.Append(d...)
		idxObjs = append(idxObjs, obj)
	}

	idxList, d := types.ListValue(types.ObjectType{AttrTypes: azureAISearchIndexAttrTypes}, idxObjs)
	diags.Append(d...)
	obj, d := types.ObjectValue(azureAISearchAttrTypes, map[string]attr.Value{"indexes": idxList})
	diags.Append(d...)
	values["azure_ai_search"] = obj
	return diags
}

func wireAzureAISearchIndex(im map[string]any) (types.Object, diag.Diagnostics) {
	topK := int64(0)
	if tk, ok := im["top_k"].(float64); ok {
		topK = int64(tk)
	}
	return types.ObjectValue(azureAISearchIndexAttrTypes, map[string]attr.Value{
		"project_connection_id": types.StringValue(stringFromMap(im, "project_connection_id")),
		"index_name":            types.StringValue(stringFromMap(im, "index_name")),
		"query_type":            types.StringValue(stringFromMap(im, "query_type")),
		"top_k":                 types.Int64Value(topK),
	})
}

func wireMemorySearchTool(toolMap map[string]any, values map[string]attr.Value) diag.Diagnostics {
	delay := int64(0)
	if d, ok := toolMap["update_delay"].(float64); ok {
		delay = int64(d)
	}
	obj, diags := types.ObjectValue(memorySearchAttrTypes, map[string]attr.Value{
		"memory_store_name": types.StringValue(stringFromMap(toolMap, "memory_store_name")),
		"scope":             types.StringValue(stringFromMap(toolMap, "scope")),
		"update_delay":      types.Int64Value(delay),
	})
	values["memory_search"] = obj
	return diags
}

// asMap returns the value as a map[string]any, or nil when v isn't one. Lets
// callers chain through the dynamically-typed wire payload without nested ok-checks.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func stringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// jsonStringFromMap re-marshals a nested map back to its JSON wire form so it
// can round-trip through a string attribute (parameters_json, spec_json, ...).
// Returns "" when the key is absent or not a map.
func jsonStringFromMap(m map[string]any, key string) string {
	sub, ok := m[key].(map[string]any)
	if !ok {
		return ""
	}
	buf, err := json.Marshal(sub)
	if err != nil {
		return ""
	}
	return string(buf)
}

func stringListFromAny(in []any) (types.List, diag.Diagnostics) {
	vals := make([]attr.Value, len(in))
	for i, v := range in {
		s, _ := v.(string)
		vals[i] = types.StringValue(s)
	}
	return types.ListValue(types.StringType, vals)
}
