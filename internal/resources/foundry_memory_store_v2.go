// Copyright (c) Your Org
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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var _ resource.Resource = &FoundryMemoryStoreV2Resource{}
var _ resource.ResourceWithImportState = &FoundryMemoryStoreV2Resource{}

func NewFoundryMemoryStoreV2Resource() resource.Resource {
	return &FoundryMemoryStoreV2Resource{}
}

type FoundryMemoryStoreV2Resource struct {
	client *client.FoundryClient
}

type FoundryMemoryStoreV2ResourceModel struct {
	// Computed
	ID        types.String `tfsdk:"id"`
	CreatedAt types.Int64  `tfsdk:"created_at"`

	// Required
	Name           types.String `tfsdk:"name"`
	ChatModel      types.String `tfsdk:"chat_model"`
	EmbeddingModel types.String `tfsdk:"embedding_model"`

	// Optional
	Description        types.String `tfsdk:"description"`
	UserProfileEnabled types.Bool   `tfsdk:"user_profile_enabled"`
	ChatSummaryEnabled types.Bool   `tfsdk:"chat_summary_enabled"`
	UserProfileDetails types.String `tfsdk:"user_profile_details"`
	Metadata           types.Map    `tfsdk:"metadata"`
}

func (r *FoundryMemoryStoreV2Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_memory_store_v2"
}

func (r *FoundryMemoryStoreV2Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `
Manages an Azure AI Foundry Memory Store (preview).

A memory store is the long-term memory backend for Foundry agents. It uses a
chat model to extract + consolidate memories from conversations and an
embedding model for similarity retrieval. Attach the store to an agent via
the ` + "`memory_search`" + ` tool on ` + "`azurefoundry_agent_v2`" + `.

~> **Preview** The memory store API is in public preview and uses
` + "`api-version=2025-11-15-preview`" + `. The shape may change before GA.
`,
		Attributes: map[string]schema.Attribute{
			// ── Computed ─────────────────────────────────────────────────────
			"id": schema.StringAttribute{
				MarkdownDescription: "The memory store ID assigned by the Foundry service.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.Int64Attribute{
				MarkdownDescription: "Unix timestamp when the memory store was created.",
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},

			// ── Required ─────────────────────────────────────────────────────
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the memory store. Unique within the project.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 256),
				},
			},
			"chat_model": schema.StringAttribute{
				MarkdownDescription: "Deployment name of the chat model used to extract and consolidate memories.",
				Required:            true,
			},
			"embedding_model": schema.StringAttribute{
				MarkdownDescription: "Deployment name of the embedding model used for memory retrieval.",
				Required:            true,
			},

			// ── Optional ─────────────────────────────────────────────────────
			"description": schema.StringAttribute{
				MarkdownDescription: "Human-readable description. Visible in the Foundry portal.",
				Optional:            true,
				Computed:            true,
			},
			"user_profile_enabled": schema.BoolAttribute{
				MarkdownDescription: "If `true`, extract user profile memories. Defaults to `false`.",
				Optional:            true,
			},
			"chat_summary_enabled": schema.BoolAttribute{
				MarkdownDescription: "If `true`, extract chat-summary memories. Defaults to `false`.",
				Optional:            true,
			},
			"user_profile_details": schema.StringAttribute{
				MarkdownDescription: "Free-form guidance to the memory system about what user-profile data to keep or avoid. Example: `\"Avoid sensitive data (age, financials, precise location, credentials).\"`",
				Optional:            true,
			},
			"metadata": schema.MapAttribute{
				MarkdownDescription: "Up to 16 key/value string pairs.",
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
			},
		},
	}
}

func (r *FoundryMemoryStoreV2Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *FoundryMemoryStoreV2Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FoundryMemoryStoreV2ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, diags := modelToCreateMemoryStoreRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating Foundry memory store", map[string]interface{}{"name": apiReq.Name})

	msResp, err := r.client.CreateMemoryStore(ctx, apiReq)
	if err != nil {
		if isConflict(err) {
			summary, detail := alreadyExistsError(
				"memory store", apiReq.Name,
				"azurefoundry_memory_store_v2", "azurefoundry:index:MemoryStoreV2",
			)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		resp.Diagnostics.AddError("Error creating memory store", err.Error())
		return
	}

	resp.Diagnostics.Append(memoryStoreResponseToModel(msResp, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryMemoryStoreV2Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FoundryMemoryStoreV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	msResp, err := r.client.GetMemoryStore(ctx, state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			tflog.Warn(ctx, "Memory store no longer exists, removing from state")
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading memory store", err.Error())
		return
	}

	resp.Diagnostics.Append(memoryStoreResponseToModel(msResp, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FoundryMemoryStoreV2Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FoundryMemoryStoreV2ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	var state FoundryMemoryStoreV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := client.UpdateMemoryStoreRequest{
		Description: plan.Description.ValueString(),
	}
	if !plan.Metadata.IsNull() && !plan.Metadata.IsUnknown() {
		meta := make(map[string]types.String, len(plan.Metadata.Elements()))
		resp.Diagnostics.Append(plan.Metadata.ElementsAs(ctx, &meta, false)...)
		metadata := make(map[string]string, len(meta))
		for k, v := range meta {
			metadata[k] = v.ValueString()
		}
		apiReq.Metadata = metadata
	}

	tflog.Debug(ctx, "Updating memory store", map[string]interface{}{"name": state.Name.ValueString()})

	msResp, err := r.client.UpdateMemoryStore(ctx, state.Name.ValueString(), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Error updating memory store", err.Error())
		return
	}

	resp.Diagnostics.Append(memoryStoreResponseToModel(msResp, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FoundryMemoryStoreV2Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FoundryMemoryStoreV2ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting memory store", map[string]interface{}{"name": state.Name.ValueString()})

	_, err := r.client.DeleteMemoryStore(ctx, state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting memory store", err.Error())
		return
	}
}

func (r *FoundryMemoryStoreV2Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	msResp, err := r.client.GetMemoryStore(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing memory store", err.Error())
		return
	}

	var state FoundryMemoryStoreV2ResourceModel
	resp.Diagnostics.Append(memoryStoreResponseToModel(msResp, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ─────────────────────────────────────────────────────────────────────────────
// Mapping helpers
// ─────────────────────────────────────────────────────────────────────────────

func modelToCreateMemoryStoreRequest(ctx context.Context, m FoundryMemoryStoreV2ResourceModel) (client.CreateMemoryStoreRequest, diag.Diagnostics) {
	var diags diag.Diagnostics

	opts := &client.MemoryStoreOptions{}
	haveOpts := false
	if !m.UserProfileEnabled.IsNull() && !m.UserProfileEnabled.IsUnknown() {
		opts.UserProfileEnabled = m.UserProfileEnabled.ValueBool()
		haveOpts = true
	}
	if !m.ChatSummaryEnabled.IsNull() && !m.ChatSummaryEnabled.IsUnknown() {
		opts.ChatSummaryEnabled = m.ChatSummaryEnabled.ValueBool()
		haveOpts = true
	}
	if !m.UserProfileDetails.IsNull() && !m.UserProfileDetails.IsUnknown() && m.UserProfileDetails.ValueString() != "" {
		opts.UserProfileDetails = m.UserProfileDetails.ValueString()
		haveOpts = true
	}

	def := client.MemoryStoreDefinition{
		Kind:           "default",
		ChatModel:      m.ChatModel.ValueString(),
		EmbeddingModel: m.EmbeddingModel.ValueString(),
	}
	if haveOpts {
		def.Options = opts
	}

	req := client.CreateMemoryStoreRequest{
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

func memoryStoreResponseToModel(r *client.MemoryStoreResponse, m *FoundryMemoryStoreV2ResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(r.ID)
	m.Name = types.StringValue(r.Name)
	m.CreatedAt = types.Int64Value(r.CreatedAt)
	m.Description = types.StringValue(r.Description)
	m.ChatModel = types.StringValue(r.Definition.ChatModel)
	m.EmbeddingModel = types.StringValue(r.Definition.EmbeddingModel)

	if r.Definition.Options != nil {
		m.UserProfileEnabled = types.BoolValue(r.Definition.Options.UserProfileEnabled)
		m.ChatSummaryEnabled = types.BoolValue(r.Definition.Options.ChatSummaryEnabled)
		if r.Definition.Options.UserProfileDetails != "" {
			m.UserProfileDetails = types.StringValue(r.Definition.Options.UserProfileDetails)
		}
	}

	if r.Metadata != nil {
		attrs := make(map[string]attr.Value, len(r.Metadata))
		for k, v := range r.Metadata {
			attrs[k] = types.StringValue(v)
		}
		mm, d := types.MapValue(types.StringType, attrs)
		diags.Append(d...)
		m.Metadata = mm
	} else {
		m.Metadata = types.MapValueMust(types.StringType, map[string]attr.Value{})
	}
	return diags
}
