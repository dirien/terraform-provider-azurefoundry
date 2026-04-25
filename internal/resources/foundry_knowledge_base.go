// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package resources

import (
	"context"
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
	_ resource.Resource                = &FoundryKnowledgeBaseResource{}
	_ resource.ResourceWithImportState = &FoundryKnowledgeBaseResource{}
)

// FoundryKnowledgeBaseResource manages an Azure AI Search Knowledge Base
// (preview). A KB references one or more `azurefoundry_knowledge_source`
// objects on the same Search service and exposes a single MCP-compatible
// `mcp_endpoint` that agents consume via the `knowledge_base` tool variant
// on `azurefoundry_agent_v2`.
type FoundryKnowledgeBaseResource struct {
	client *client.FoundryClient
}

func NewFoundryKnowledgeBaseResource() resource.Resource {
	return &FoundryKnowledgeBaseResource{}
}

type FoundryKnowledgeBaseResourceModel struct {
	ID                       types.String `tfsdk:"id"`
	Name                     types.String `tfsdk:"name"`
	SearchEndpoint           types.String `tfsdk:"search_endpoint"`
	Description              types.String `tfsdk:"description"`
	RetrievalInstructions    types.String `tfsdk:"retrieval_instructions"`
	AnswerInstructions       types.String `tfsdk:"answer_instructions"`
	OutputMode               types.String `tfsdk:"output_mode"`
	ETag                     types.String `tfsdk:"etag"`
	MCPEndpoint              types.String `tfsdk:"mcp_endpoint"`
	KnowledgeSources         types.List   `tfsdk:"knowledge_sources"`
	Models                   types.List   `tfsdk:"models"`
	RetrievalReasoningEffort types.Object `tfsdk:"retrieval_reasoning_effort"`
}

var kbKnowledgeSourceRefAttrTypes = map[string]attr.Type{
	"name": types.StringType,
}

var kbAzureOpenAIAttrTypes = map[string]attr.Type{
	"resource_uri":              types.StringType,
	"deployment_id":             types.StringType,
	"model_name":                types.StringType,
	"api_key":                   types.StringType,
	"user_assigned_identity_id": types.StringType,
}

var kbModelAttrTypes = map[string]attr.Type{
	"azure_open_ai": types.ObjectType{AttrTypes: kbAzureOpenAIAttrTypes},
}

var kbReasoningEffortAttrTypes = map[string]attr.Type{
	"kind": types.StringType,
}

func (r *FoundryKnowledgeBaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_knowledge_base"
}

func (r *FoundryKnowledgeBaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Azure AI Search **Knowledge Base** (preview, " +
			"`api-version=2025-11-01-preview`).\n\n" +
			"A knowledge base bundles one or more `azurefoundry_knowledge_source` objects behind " +
			"a single MCP endpoint. Foundry agents consume it through the typed `knowledge_base` " +
			"tool variant on `azurefoundry_agent_v2.tools[*]` (or by wiring the computed " +
			"`mcp_endpoint` into a raw `mcp` tool block — the typed variant just expands to that).\n\n" +
			"### Wiring it to an agent\n" +
			"The MCP traffic is authenticated via a project connection on the Foundry side, not " +
			"managed by this provider. Create one with `category = \"RemoteTool\"`, " +
			"`authType = \"ProjectManagedIdentity\"`, `audience = \"https://search.azure.com/\"`, and " +
			"`target = <kb.mcp_endpoint>` using:\n\n" +
			"- **Terraform:** [`azurerm_cognitive_account_project_connection`]" +
			"(https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cognitive_account_project_connection)\n" +
			"- **Pulumi:** [`azure-native:cognitiveservices:Connection`]" +
			"(https://www.pulumi.com/registry/packages/azure-native/api-docs/cognitiveservices/connection/)\n\n" +
			"Then reference that connection's name from the agent's `knowledge_base` tool block. " +
			"See the example for the full setup including required RBAC.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Synthetic ID `<search_endpoint>|<name>`. Used by `terraform import`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Knowledge base name. Unique within the Search service. Changing this forces replacement.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"search_endpoint": schema.StringAttribute{
				MarkdownDescription: "Azure AI Search service endpoint, e.g. `https://my-search.search.windows.net`. Per-resource so a single provider configuration can manage knowledge bases across multiple Search services. Changing this forces replacement.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional human-readable description.",
				Optional:            true,
				Computed:            true,
			},
			"retrieval_instructions": schema.StringAttribute{
				MarkdownDescription: "Steers the LLM during query planning — when each knowledge source is in scope. Behaves like a prompt fragment; you can include brevity, tone, and inclusion/exclusion guidance.",
				Optional:            true,
				Computed:            true,
			},
			"answer_instructions": schema.StringAttribute{
				MarkdownDescription: "Steers answer synthesis. Only meaningful when `output_mode = \"answerSynthesis\"`.",
				Optional:            true,
				Computed:            true,
			},
			"output_mode": schema.StringAttribute{
				MarkdownDescription: "`extractiveData` returns matched chunks unchanged; `answerSynthesis` returns a synthesized answer alongside citations. Defaults to the Search service default.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(client.KBOutputModeExtractiveData, client.KBOutputModeAnswerSynthesis),
				},
			},
			"etag": schema.StringAttribute{
				MarkdownDescription: "Service-assigned ETag (`@odata.etag`). Updated on every write.",
				Computed:            true,
			},
			"mcp_endpoint": schema.StringAttribute{
				MarkdownDescription: "MCP-compatible URL agents wire into the `knowledge_base` tool variant on `azurefoundry_agent_v2`. Always pinned to `api-version=2025-11-01-preview`.",
				Computed:            true,
			},
			"knowledge_sources": schema.ListNestedAttribute{
				MarkdownDescription: "Knowledge sources backing this KB. Each entry references an existing `azurefoundry_knowledge_source` on the same Search service, by name.",
				Required:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Knowledge source name (`azurefoundry_knowledge_source.X.name`).",
						},
					},
				},
			},
			"models": schema.ListNestedAttribute{
				MarkdownDescription: "AI models used for query planning during retrieval. Today only `azure_open_ai` is supported. Required when `retrieval_reasoning_effort.kind` is `low` or `medium`; ignored for `minimal`.",
				Optional:            true,
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"azure_open_ai": schema.SingleNestedAttribute{
							MarkdownDescription: "Azure OpenAI deployment used for query planning.",
							Required:            true,
							Attributes: map[string]schema.Attribute{
								"resource_uri": schema.StringAttribute{
									MarkdownDescription: "AOAI resource URI, e.g. `https://my-aoai.openai.azure.com/`.",
									Required:            true,
								},
								"deployment_id": schema.StringAttribute{
									MarkdownDescription: "Deployment name on the AOAI resource.",
									Required:            true,
								},
								"model_name": schema.StringAttribute{
									MarkdownDescription: "Underlying model name, e.g. `gpt-4o-mini` or `gpt-4.1-mini`.",
									Optional:            true,
								},
								"api_key": schema.StringAttribute{
									MarkdownDescription: "AOAI API key for the deployment. Mutually exclusive with `user_assigned_identity_id`. Sensitive.",
									Optional:            true,
									Sensitive:           true,
								},
								"user_assigned_identity_id": schema.StringAttribute{
									MarkdownDescription: "Full ARM ID of a user-assigned managed identity authorized on the AOAI resource. When unset and `api_key` is also unset, Search uses its own system-assigned managed identity.",
									Optional:            true,
								},
							},
						},
					},
				},
			},
			"retrieval_reasoning_effort": schema.SingleNestedAttribute{
				MarkdownDescription: "Controls how much LLM reasoning is applied during retrieval planning. `minimal` skips planning entirely (every source is in scope every query); `low` and `medium` use the model in `models[]` to pick sources at query time.",
				Optional:            true,
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{
						MarkdownDescription: "`minimal`, `low`, or `medium`.",
						Required:            true,
						Validators: []validator.String{
							stringvalidator.OneOf(client.KBReasoningEffortMinimal, client.KBReasoningEffortLow, client.KBReasoningEffortMedium),
						},
					},
				},
			},
		},
	}
}

func (r *FoundryKnowledgeBaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *FoundryKnowledgeBaseResource) searchClient() (*client.SearchClient, diag.Diagnostics) {
	var diags diag.Diagnostics
	sc, err := r.client.SearchClient()
	if err != nil {
		diags.AddError("Search client unavailable", err.Error())
		return nil, diags
	}
	return sc, diags
}

func (r *FoundryKnowledgeBaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FoundryKnowledgeBaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wire, diags := buildKnowledgeBaseWire(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating Foundry knowledge base", map[string]any{
		"name":            plan.Name.ValueString(),
		"search_endpoint": plan.SearchEndpoint.ValueString(),
	})

	resp.Diagnostics.Append(r.preflightKBMustNotExist(ctx, sc, plan.SearchEndpoint.ValueString(), plan.Name.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := sc.CreateOrUpdateKnowledgeBase(ctx, plan.SearchEndpoint.ValueString(), wire)
	if err != nil {
		resp.Diagnostics.AddError("Error creating knowledge base", err.Error())
		return
	}

	resp.Diagnostics.Append(applyKnowledgeBaseResponse(ctx, result, &plan, plan.SearchEndpoint.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryKnowledgeBaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FoundryKnowledgeBaseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := sc.GetKnowledgeBase(ctx, state.SearchEndpoint.ValueString(), state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			tflog.Warn(ctx, "Knowledge base no longer exists, removing from state")
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading knowledge base", err.Error())
		return
	}

	resp.Diagnostics.Append(applyKnowledgeBaseResponse(ctx, result, &state, state.SearchEndpoint.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryKnowledgeBaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FoundryKnowledgeBaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wire, diags := buildKnowledgeBaseWire(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := sc.CreateOrUpdateKnowledgeBase(ctx, plan.SearchEndpoint.ValueString(), wire)
	if err != nil {
		resp.Diagnostics.AddError("Error updating knowledge base", err.Error())
		return
	}

	resp.Diagnostics.Append(applyKnowledgeBaseResponse(ctx, result, &plan, plan.SearchEndpoint.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryKnowledgeBaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FoundryKnowledgeBaseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := sc.DeleteKnowledgeBase(ctx, state.SearchEndpoint.ValueString(), state.Name.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting knowledge base", err.Error())
	}
}

func (r *FoundryKnowledgeBaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Expected `<search_endpoint>|<name>`, e.g. `https://my-search.search.windows.net|fraud-policy-kb`.",
		)
		return
	}
	searchEndpoint, name := parts[0], parts[1]

	sc, diags := r.searchClient()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := sc.GetKnowledgeBase(ctx, searchEndpoint, name)
	if err != nil {
		resp.Diagnostics.AddError("Error importing knowledge base", err.Error())
		return
	}

	state := FoundryKnowledgeBaseResourceModel{
		SearchEndpoint: types.StringValue(searchEndpoint),
	}
	resp.Diagnostics.Append(applyKnowledgeBaseResponse(ctx, result, &state, searchEndpoint)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryKnowledgeBaseResource) preflightKBMustNotExist(ctx context.Context, sc *client.SearchClient, searchEndpoint, name string) diag.Diagnostics {
	var diags diag.Diagnostics
	existing, err := sc.GetKnowledgeBase(ctx, searchEndpoint, name)
	switch {
	case err == nil && existing != nil:
		summary, detail := alreadyExistsError(
			"knowledge base", name,
			"azurefoundry_knowledge_base", "azurefoundry:index:KnowledgeBase",
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

func buildKnowledgeBaseWire(ctx context.Context, m FoundryKnowledgeBaseResourceModel) (client.KnowledgeBaseWire, diag.Diagnostics) {
	var diags diag.Diagnostics
	wire := client.KnowledgeBaseWire{
		Name:                  m.Name.ValueString(),
		Description:           m.Description.ValueString(),
		RetrievalInstructions: m.RetrievalInstructions.ValueString(),
		AnswerInstructions:    m.AnswerInstructions.ValueString(),
		OutputMode:            m.OutputMode.ValueString(),
	}

	wire.KnowledgeSources = extractKnowledgeSourceRefs(ctx, m.KnowledgeSources)

	models, d := extractKBModels(ctx, m.Models)
	diags.Append(d...)
	wire.Models = models

	if !m.RetrievalReasoningEffort.IsNull() && !m.RetrievalReasoningEffort.IsUnknown() {
		kind := stringAttr(m.RetrievalReasoningEffort.Attributes(), "kind")
		if kind != "" {
			wire.RetrievalReasoningEffort = &client.KnowledgeRetrievalReasoning{Kind: kind}
		}
	}
	return wire, diags
}

func extractKnowledgeSourceRefs(_ context.Context, l types.List) []client.KnowledgeSourceRef {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]client.KnowledgeSourceRef, 0, len(l.Elements()))
	for _, elem := range l.Elements() {
		obj, ok := elem.(types.Object)
		if !ok {
			continue
		}
		out = append(out, client.KnowledgeSourceRef{Name: stringAttr(obj.Attributes(), "name")})
	}
	return out
}

func extractKBModels(_ context.Context, l types.List) ([]client.KnowledgeBaseModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	if l.IsNull() || l.IsUnknown() {
		return nil, diags
	}
	out := make([]client.KnowledgeBaseModel, 0, len(l.Elements()))
	for _, elem := range l.Elements() {
		obj, ok := elem.(types.Object)
		if !ok {
			continue
		}
		aoaiObj, ok := obj.Attributes()["azure_open_ai"].(types.Object)
		if !ok || aoaiObj.IsNull() || aoaiObj.IsUnknown() {
			diags.AddError("models[*] missing azure_open_ai", "Each entry in `models` must include an `azure_open_ai` block.")
			continue
		}
		params := buildAOAIParams(aoaiObj.Attributes())
		out = append(out, client.KnowledgeBaseModel{
			Kind:                  client.KBModelKindAzureOpenAI,
			AzureOpenAIParameters: params,
		})
	}
	return out, diags
}

func buildAOAIParams(a map[string]attr.Value) *client.AzureOpenAIVectorizerCfg {
	cfg := &client.AzureOpenAIVectorizerCfg{
		ResourceURI:  stringAttr(a, "resource_uri"),
		DeploymentID: stringAttr(a, "deployment_id"),
		ModelName:    stringAttr(a, "model_name"),
		APIKey:       stringAttr(a, "api_key"),
	}
	if uami := stringAttr(a, "user_assigned_identity_id"); uami != "" {
		cfg.AuthIdentity = &client.SearchIndexerIdentity{
			ODataType:            "#Microsoft.Azure.Search.DataUserAssignedIdentity",
			UserAssignedIdentity: uami,
		}
	}
	return cfg
}

func applyKnowledgeBaseResponse(ctx context.Context, r *client.KnowledgeBaseWire, m *FoundryKnowledgeBaseResourceModel, searchEndpoint string) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(searchEndpoint + "|" + r.Name)
	m.Name = types.StringValue(r.Name)
	m.Description = types.StringValue(r.Description)
	m.RetrievalInstructions = types.StringValue(r.RetrievalInstructions)
	m.AnswerInstructions = types.StringValue(r.AnswerInstructions)
	m.OutputMode = types.StringValue(r.OutputMode)
	m.ETag = types.StringValue(r.ETag)
	m.MCPEndpoint = types.StringValue(client.KnowledgeBaseMCPEndpoint(searchEndpoint, r.Name))

	ksList, d := knowledgeSourceRefsToList(r.KnowledgeSources)
	diags.Append(d...)
	m.KnowledgeSources = ksList

	modelList, d := kbModelsToList(ctx, r.Models, m.Models)
	diags.Append(d...)
	m.Models = modelList

	if r.RetrievalReasoningEffort != nil {
		obj, d := types.ObjectValue(kbReasoningEffortAttrTypes, map[string]attr.Value{
			"kind": types.StringValue(r.RetrievalReasoningEffort.Kind),
		})
		diags.Append(d...)
		m.RetrievalReasoningEffort = obj
	} else {
		m.RetrievalReasoningEffort = types.ObjectNull(kbReasoningEffortAttrTypes)
	}
	return diags
}

func knowledgeSourceRefsToList(refs []client.KnowledgeSourceRef) (types.List, diag.Diagnostics) {
	objs := make([]attr.Value, 0, len(refs))
	var diags diag.Diagnostics
	for _, ref := range refs {
		obj, d := types.ObjectValue(kbKnowledgeSourceRefAttrTypes, map[string]attr.Value{
			"name": types.StringValue(ref.Name),
		})
		diags.Append(d...)
		objs = append(objs, obj)
	}
	list, d := types.ListValue(types.ObjectType{AttrTypes: kbKnowledgeSourceRefAttrTypes}, objs)
	diags.Append(d...)
	return list, diags
}

// kbModelsToList preserves user-supplied secrets (api_key) from prior state.
// Search redacts api_key on GET responses, so blindly trusting the wire
// would constantly produce drift. Same redaction handling we use for the
// blob KS connection_string.
func kbModelsToList(_ context.Context, models []client.KnowledgeBaseModel, prior types.List) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	priorAPIKeys := make([]string, len(models))
	if !prior.IsNull() && !prior.IsUnknown() {
		for i, elem := range prior.Elements() {
			if i >= len(priorAPIKeys) {
				break
			}
			obj, ok := elem.(types.Object)
			if !ok {
				continue
			}
			aoaiObj, ok := obj.Attributes()["azure_open_ai"].(types.Object)
			if !ok {
				continue
			}
			priorAPIKeys[i] = stringAttr(aoaiObj.Attributes(), "api_key")
		}
	}

	objs := make([]attr.Value, 0, len(models))
	for i, model := range models {
		if model.AzureOpenAIParameters == nil {
			continue
		}
		aoai := model.AzureOpenAIParameters
		apiKey := aoai.APIKey
		if apiKey == "" && i < len(priorAPIKeys) {
			apiKey = priorAPIKeys[i]
		}
		uami := ""
		if aoai.AuthIdentity != nil {
			uami = aoai.AuthIdentity.UserAssignedIdentity
		}
		aoaiObj, d := types.ObjectValue(kbAzureOpenAIAttrTypes, map[string]attr.Value{
			"resource_uri":              types.StringValue(aoai.ResourceURI),
			"deployment_id":             types.StringValue(aoai.DeploymentID),
			"model_name":                types.StringValue(aoai.ModelName),
			"api_key":                   types.StringValue(apiKey),
			"user_assigned_identity_id": types.StringValue(uami),
		})
		diags.Append(d...)
		obj, d := types.ObjectValue(kbModelAttrTypes, map[string]attr.Value{
			"azure_open_ai": aoaiObj,
		})
		diags.Append(d...)
		objs = append(objs, obj)
	}
	list, d := types.ListValue(types.ObjectType{AttrTypes: kbModelAttrTypes}, objs)
	diags.Append(d...)
	return list, diags
}
