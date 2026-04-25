// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package resources

import (
	"context"
	"fmt"

	"github.com/dirien/terraform-provider-azurefoundry/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &FoundryToolboxV2Resource{}
	_ resource.ResourceWithImportState = &FoundryToolboxV2Resource{}
)

// FoundryToolboxV2Resource manages a Foundry Toolbox (preview).
//
// Wire-level mapping. Foundry models a toolbox as a parent ToolboxObject
// with one or more immutable ToolboxVersionObject children. Versions are
// append-only — every Update posts a new version and (when promote_default
// is true) flips the parent's default_version to point at it. Old versions
// are not deleted unless prune_old_versions is true; this matches the
// Foundry-recommended workflow of staging changes against the version-
// specific endpoint before promoting them on the consumer endpoint.
//
// One Terraform resource binds 1:1 to a toolbox name and tracks one
// "current" version (the version this apply produced). The version_id
// attribute reflects that pin so terraform plan / drift detection compares
// against the version this state created, not whatever's currently default.
type FoundryToolboxV2Resource struct {
	client *client.FoundryClient
}

func NewFoundryToolboxV2Resource() resource.Resource {
	return &FoundryToolboxV2Resource{}
}

type FoundryToolboxV2ResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	Tools             types.List   `tfsdk:"tools"`
	PromoteDefault    types.Bool   `tfsdk:"promote_default"`
	PruneOldVersions  types.Bool   `tfsdk:"prune_old_versions"`
	VersionID         types.String `tfsdk:"version_id"`
	DefaultVersion    types.String `tfsdk:"default_version"`
	CreatedAt         types.Int64  `tfsdk:"created_at"`
	ConsumerEndpoint  types.String `tfsdk:"consumer_endpoint"`
	VersionedEndpoint types.String `tfsdk:"versioned_endpoint"`
}

func (r *FoundryToolboxV2Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_toolbox_v2"
}

func (r *FoundryToolboxV2Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Azure AI Foundry Toolbox (preview).\n\n" +
			"A toolbox is a project-scoped, MCP-compatible bundle of tools that " +
			"agents consume by URL. Use it to share one configured set of tools " +
			"across multiple `azurefoundry_agent_v2` resources without " +
			"duplicating inline definitions. Toolboxes also surface in the " +
			"Foundry portal under **Build → Tools → Toolboxes**.\n\n" +
			"### Versioning\n" +
			"Foundry persists each Update as a new immutable version. By default " +
			"this resource promotes the version it just produced to " +
			"`default_version` so the consumer endpoint serves it. Set " +
			"`promote_default = false` to publish a new version *without* " +
			"flipping the default — useful for canary-style validation against " +
			"`versioned_endpoint` before promoting in a follow-up apply.\n\n" +
			"### Consumption\n" +
			"Wire `consumer_endpoint` into an `azurefoundry_agent_v2` `mcp` tool " +
			"block. The provider sets the required `Foundry-Features: " +
			"Toolboxes=V1Preview` header on every request, but agent runtimes " +
			"that consume the endpoint at inference time must set it " +
			"themselves.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Foundry-assigned toolbox ID, or the toolbox name when no separate ID is returned.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "User-supplied toolbox name. Unique within the Foundry project. Changing this forces replacement.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Human-readable description, surfaced in the Foundry portal and stored on each version.",
				Optional:            true,
				Computed:            true,
			},
			"promote_default": schema.BoolAttribute{
				MarkdownDescription: "When `true` (default), every new version produced by Create/Update is " +
					"promoted to the toolbox's `default_version` so the consumer endpoint serves it. " +
					"Set to `false` to publish a version without flipping the default — useful when " +
					"validating a new version against `versioned_endpoint` before promoting in a follow-up apply.",
				Optional: true,
				Computed: true,
			},
			"prune_old_versions": schema.BoolAttribute{
				MarkdownDescription: "When `true`, after a successful Update the previously-managed version is deleted. " +
					"Foundry refuses to delete the version currently set as `default_version`; with " +
					"`promote_default = false` and `prune_old_versions = true` set together, the previous version " +
					"is kept (Foundry-side) until a later apply promotes a new default. Defaults to `false`.",
				Optional: true,
				Computed: true,
			},
			"version_id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the version this Terraform state pinned (the version Create or the most recent Update produced). " +
					"Drift detection compares against this — not the toolbox's currently-promoted `default_version`.",
				Computed: true,
			},
			"default_version": schema.StringAttribute{
				MarkdownDescription: "The toolbox's currently promoted `default_version` as last observed by the provider. " +
					"Equals `version_id` when `promote_default` is `true`; may diverge when versions are promoted out-of-band.",
				Computed: true,
			},
			"created_at": schema.Int64Attribute{
				MarkdownDescription: "Unix timestamp (seconds) when the pinned version was created.",
				Computed:            true,
			},
			"consumer_endpoint": schema.StringAttribute{
				MarkdownDescription: "MCP-compatible URL agents wire into a `tools { type = \"mcp\" }` block. " +
					"Always serves the toolbox's promoted `default_version`. Of the two endpoints, this is the " +
					"one to use for production agents.",
				Computed: true,
			},
			"versioned_endpoint": schema.StringAttribute{
				MarkdownDescription: "MCP-compatible URL pinned to the version recorded in `version_id`. " +
					"Use it to validate a new version with an MCP client before flipping `default_version`.",
				Computed: true,
			},
		},
		Blocks: map[string]schema.Block{
			"tools": schema.ListNestedBlock{
				MarkdownDescription: "Tools embedded in this toolbox version. Same nested-block shape as " +
					"`azurefoundry_agent_v2.tools[*]`; the provider reuses the same dispatch helpers so " +
					"every tool variant supported on agents (`mcp`, `openapi`, `function`, `web_search`, " +
					"`file_search`, `code_interpreter`, `azure_ai_search`, `bing_grounding`, `memory_search`) " +
					"works here as well. Foundry validates the variant server-side; unsupported types return 400.\n\n" +
					"Each Update replaces the entire tools list and posts a new immutable version.",
				NestedObject: toolboxToolsNestedBlock(),
			},
		},
	}
}

// toolboxToolsNestedBlock mirrors the nested-block layout used by
// azurefoundry_agent_v2 so the shared toolExtractors / toolWirers can
// dispatch the same wire variants for toolbox tools. Kept inline (rather
// than reaching into the agent resource's Schema) so the two resources
// stay decoupled at compile time.
func toolboxToolsNestedBlock() schema.NestedBlockObject {
	return schema.NestedBlockObject{
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{
				MarkdownDescription: "Tool type. See `azurefoundry_agent_v2.tools[*].type` for the full list of supported variants.",
				Required:            true,
			},
			"vector_store_ids": schema.ListAttribute{
				MarkdownDescription: "Vector store IDs to search. Used when `type = \"file_search\"`.",
				Optional:            true,
				ElementType:         types.StringType,
			},
			"max_num_results": schema.Int64Attribute{
				MarkdownDescription: "Maximum search hits returned per query. Used when `type = \"file_search\"`.",
				Optional:            true,
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
				MarkdownDescription: "OpenAI-style function calling. Used when `type = \"function\"`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"name":            schema.StringAttribute{MarkdownDescription: "Function name the model will call.", Required: true},
					"description":     schema.StringAttribute{MarkdownDescription: "Human-readable description shown to the model.", Optional: true},
					"parameters_json": schema.StringAttribute{MarkdownDescription: "JSON Schema (as a string) for the function's parameters.", Optional: true},
				},
			},
			"openapi": schema.SingleNestedAttribute{
				MarkdownDescription: "OpenAPI spec inlined as a tool. Used when `type = \"openapi\"`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"name":        schema.StringAttribute{MarkdownDescription: "Tool name surfaced to the model.", Required: true},
					"description": schema.StringAttribute{MarkdownDescription: "Human-readable description shown to the model.", Optional: true},
					"spec_json":   schema.StringAttribute{MarkdownDescription: "OpenAPI 3.x spec serialized as a JSON string.", Required: true},
					"auth_type":   schema.StringAttribute{MarkdownDescription: "`anonymous` (default) or `connection`.", Optional: true},
				},
			},
			"mcp": schema.SingleNestedAttribute{
				MarkdownDescription: "Model Context Protocol server. Used when `type = \"mcp\"`. Most common toolbox variant — pair with a `RemoteTool`-category project connection for authenticated upstreams.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"server_label":          schema.StringAttribute{MarkdownDescription: "Display label for the MCP server in tool-call traces.", Required: true},
					"server_url":            schema.StringAttribute{MarkdownDescription: "URL of the MCP server. Must be reachable from Foundry's egress.", Required: true},
					"require_approval":      schema.StringAttribute{MarkdownDescription: "`always`, `never`, or omitted (Foundry default).", Optional: true},
					"project_connection_id": schema.StringAttribute{MarkdownDescription: "Project connection ID used to authenticate to the MCP server.", Optional: true},
				},
			},
			"azure_ai_search": schema.SingleNestedAttribute{
				MarkdownDescription: "Azure AI Search retrieval. Used when `type = \"azure_ai_search\"`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"indexes": schema.ListNestedAttribute{
						MarkdownDescription: "Indexes to query.",
						Required:            true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"project_connection_id": schema.StringAttribute{MarkdownDescription: "Project connection ID for the Azure AI Search account.", Required: true},
								"index_name":            schema.StringAttribute{MarkdownDescription: "Name of the index within the search account.", Required: true},
								"query_type":            schema.StringAttribute{MarkdownDescription: "`simple`, `semantic`, `vector`, `vector_simple_hybrid`, or `vector_semantic_hybrid`.", Optional: true},
								"top_k":                 schema.Int64Attribute{MarkdownDescription: "Maximum hits returned per query.", Optional: true},
							},
						},
					},
				},
			},
			"bing_grounding": schema.SingleNestedAttribute{
				MarkdownDescription: "Bing Search v7 grounding via a project connection. Used when `type = \"bing_grounding\"`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"connection_id": schema.StringAttribute{MarkdownDescription: "Project connection ID bound to the Bing Search resource.", Required: true},
				},
			},
			"memory_search": schema.SingleNestedAttribute{
				MarkdownDescription: "Attach a Foundry Memory store (preview). Used when `type = \"memory_search\"`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"memory_store_name": schema.StringAttribute{MarkdownDescription: "Name of the `azurefoundry_memory_store_v2` to attach.", Required: true},
					"scope":             schema.StringAttribute{MarkdownDescription: "Memory scope expression.", Optional: true},
					"update_delay":      schema.Int64Attribute{MarkdownDescription: "Seconds of inactivity before extracted memories are written back.", Optional: true},
				},
			},
		},
	}
}

func (r *FoundryToolboxV2Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *FoundryToolboxV2Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FoundryToolboxV2ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tools, diags := extractV2Tools(ctx, plan.Tools)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating Foundry toolbox version", map[string]any{"name": plan.Name.ValueString()})

	// Pre-flight: if the toolbox already exists Terraform should ask the
	// user to import it rather than silently appending a new version onto
	// it. Foundry's POST /versions auto-creates the parent on first version
	// but happily appends to an existing one — that's the path we want to
	// trip here.
	resp.Diagnostics.Append(r.preflightToolboxMustNotExist(ctx, plan.Name.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}

	versionResp, err := r.client.CreateToolboxVersion(ctx, plan.Name.ValueString(), client.CreateToolboxVersionRequest{
		Description: plan.Description.ValueString(),
		Tools:       tools,
	})
	if err != nil {
		if isConflict(err) {
			summary, detail := alreadyExistsError(
				"toolbox", plan.Name.ValueString(),
				"azurefoundry_toolbox_v2", "azurefoundry:index:ToolboxV2",
			)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		resp.Diagnostics.AddError("Error creating Foundry toolbox", err.Error())
		return
	}

	// First version of a previously-unseen toolbox is auto-promoted by
	// Foundry, so we only need to call PATCH /toolboxes/{name} when the
	// caller asked for promotion AND we know the version isn't already
	// default. For a true Create on a fresh name that's a no-op; for an
	// import or a Create-after-Delete-of-versions-only it's the safety net.
	defaultVersion := versionResp.Version
	if r.shouldPromote(plan) {
		tb, err := r.client.PromoteToolboxVersion(ctx, plan.Name.ValueString(), versionResp.Version)
		if err != nil {
			resp.Diagnostics.AddError("Error promoting Foundry toolbox version", err.Error())
			return
		}
		defaultVersion = tb.DefaultVersion
	}

	r.populateModel(&plan, versionResp, defaultVersion)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryToolboxV2Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FoundryToolboxV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	versionResp, err := r.client.GetToolboxVersion(ctx, state.Name.ValueString(), state.VersionID.ValueString())
	if err != nil {
		if isNotFound(err) {
			tflog.Warn(ctx, "Foundry toolbox version no longer exists, removing from state", map[string]any{
				"name":       state.Name.ValueString(),
				"version_id": state.VersionID.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Foundry toolbox version", err.Error())
		return
	}

	defaultVersion := state.DefaultVersion.ValueString()
	if tb, err := r.client.GetToolbox(ctx, state.Name.ValueString()); err == nil && tb != nil && tb.DefaultVersion != "" {
		defaultVersion = tb.DefaultVersion
	}

	r.populateModel(&state, versionResp, defaultVersion)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryToolboxV2Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FoundryToolboxV2ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	var state FoundryToolboxV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tools, diags := extractV2Tools(ctx, plan.Tools)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	previousVersion := state.VersionID.ValueString()
	tflog.Debug(ctx, "Updating Foundry toolbox", map[string]any{
		"name":             plan.Name.ValueString(),
		"previous_version": previousVersion,
	})

	versionResp, err := r.client.CreateToolboxVersion(ctx, plan.Name.ValueString(), client.CreateToolboxVersionRequest{
		Description: plan.Description.ValueString(),
		Tools:       tools,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error creating new Foundry toolbox version", err.Error())
		return
	}

	defaultVersion := state.DefaultVersion.ValueString()
	if r.shouldPromote(plan) {
		tb, err := r.client.PromoteToolboxVersion(ctx, plan.Name.ValueString(), versionResp.Version)
		if err != nil {
			resp.Diagnostics.AddError("Error promoting Foundry toolbox version", err.Error())
			return
		}
		defaultVersion = tb.DefaultVersion
	}

	// Optional cleanup of the previous version. Foundry refuses to delete
	// the version that's currently default, so this is safe even when
	// promote_default is false: in that case the previous version is also
	// the current default and Foundry will return 409, which we surface as
	// a non-fatal warning rather than rolling back the new version.
	if r.shouldPrune(plan) && previousVersion != "" && previousVersion != versionResp.Version {
		if err := r.client.DeleteToolboxVersion(ctx, plan.Name.ValueString(), previousVersion); err != nil {
			tflog.Warn(ctx, "Failed to prune previous toolbox version (continuing)", map[string]any{
				"name":             plan.Name.ValueString(),
				"previous_version": previousVersion,
				"error":            err.Error(),
			})
		}
	}

	r.populateModel(&plan, versionResp, defaultVersion)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryToolboxV2Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FoundryToolboxV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting Foundry toolbox", map[string]any{"name": state.Name.ValueString()})

	if err := r.client.DeleteToolbox(ctx, state.Name.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting Foundry toolbox", err.Error())
		return
	}
}

func (r *FoundryToolboxV2Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tb, err := r.client.GetToolbox(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing Foundry toolbox", err.Error())
		return
	}
	if tb.DefaultVersion == "" {
		resp.Diagnostics.AddError(
			"Toolbox has no default_version",
			fmt.Sprintf("Toolbox %q exists but has no default_version set; cannot determine which version to import. "+
				"Promote a version manually and retry.", req.ID),
		)
		return
	}

	versionResp, err := r.client.GetToolboxVersion(ctx, req.ID, tb.DefaultVersion)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Foundry toolbox version on import", err.Error())
		return
	}

	state := FoundryToolboxV2ResourceModel{
		PromoteDefault:   types.BoolValue(true),
		PruneOldVersions: types.BoolValue(false),
	}
	state.Name = types.StringValue(tb.Name)
	r.populateModel(&state, versionResp, tb.DefaultVersion)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// preflightToolboxMustNotExist mirrors the agent_v2 conflict-recovery
// pattern. Foundry's POST /toolboxes/{name}/versions auto-creates the
// parent on the first version but happily appends a new version onto an
// existing one — which would silently merge state from a prior orphaned
// create. Detect the orphan up-front and surface the import hint instead.
func (r *FoundryToolboxV2Resource) preflightToolboxMustNotExist(ctx context.Context, name string) diag.Diagnostics {
	var diags diag.Diagnostics
	existing, err := r.client.GetToolbox(ctx, name)
	switch {
	case err == nil && existing != nil:
		summary, detail := alreadyExistsError(
			"toolbox", name,
			"azurefoundry_toolbox_v2", "azurefoundry:index:ToolboxV2",
		)
		diags.AddError(summary, detail)
	case err != nil && !isNotFound(err):
		diags.AddError("Pre-flight existence check failed", err.Error())
	}
	return diags
}

func (r *FoundryToolboxV2Resource) shouldPromote(m FoundryToolboxV2ResourceModel) bool {
	if m.PromoteDefault.IsNull() || m.PromoteDefault.IsUnknown() {
		return true // default
	}
	return m.PromoteDefault.ValueBool()
}

func (r *FoundryToolboxV2Resource) shouldPrune(m FoundryToolboxV2ResourceModel) bool {
	if m.PruneOldVersions.IsNull() || m.PruneOldVersions.IsUnknown() {
		return false
	}
	return m.PruneOldVersions.ValueBool()
}

func (r *FoundryToolboxV2Resource) populateModel(m *FoundryToolboxV2ResourceModel, v *client.ToolboxVersionObject, defaultVersion string) {
	if v.ID != "" {
		m.ID = types.StringValue(v.ID)
	} else {
		m.ID = types.StringValue(v.Name)
	}
	m.Name = types.StringValue(v.Name)
	m.Description = types.StringValue(v.Description)
	m.VersionID = types.StringValue(v.Version)
	m.DefaultVersion = types.StringValue(defaultVersion)
	m.CreatedAt = types.Int64Value(v.CreatedAt)

	if m.PromoteDefault.IsNull() || m.PromoteDefault.IsUnknown() {
		m.PromoteDefault = types.BoolValue(true)
	}
	if m.PruneOldVersions.IsNull() || m.PruneOldVersions.IsUnknown() {
		m.PruneOldVersions = types.BoolValue(false)
	}

	m.ConsumerEndpoint = types.StringValue(r.client.ToolboxConsumerEndpoint(v.Name))
	m.VersionedEndpoint = types.StringValue(r.client.ToolboxVersionedEndpoint(v.Name, v.Version))

	toolObjects := make([]attr.Value, 0, len(v.Tools))
	for _, t := range v.Tools {
		toolMap, ok := t.(map[string]any)
		if !ok {
			continue
		}
		obj, _ := wireToolToObject(toolMap)
		toolObjects = append(toolObjects, obj)
	}
	toolList, _ := types.ListValue(types.ObjectType{AttrTypes: toolAttrTypesV2}, toolObjects)
	m.Tools = toolList
}
