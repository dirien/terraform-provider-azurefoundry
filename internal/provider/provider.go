// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"os"

	"github.com/dirien/terraform-provider-azurefoundry/internal/client"
	"github.com/dirien/terraform-provider-azurefoundry/internal/resources"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ provider.Provider              = &AzureFoundryProvider{}
	_ provider.ProviderWithFunctions = &AzureFoundryProvider{}
)

type AzureFoundryProvider struct {
	version string
}

type AzureFoundryProviderModel struct {
	ProjectEndpoint types.String `tfsdk:"project_endpoint"`
	TenantID        types.String `tfsdk:"tenant_id"`
	ClientID        types.String `tfsdk:"client_id"`
	ClientSecret    types.String `tfsdk:"client_secret"`
	OIDCToken       types.String `tfsdk:"oidc_token"`
	APIKey          types.String `tfsdk:"api_key"`
	UseAzureCLI     types.Bool   `tfsdk:"use_azure_cli"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &AzureFoundryProvider{version: version}
	}
}

func (p *AzureFoundryProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "azurefoundry"
	resp.Version = p.version
}

func (p *AzureFoundryProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The azurefoundry provider manages resources in Azure AI Foundry.",
		Attributes: map[string]schema.Attribute{
			"project_endpoint": schema.StringAttribute{
				MarkdownDescription: "The Azure AI Foundry project endpoint. " +
					"Can also be set via `AZURE_AI_FOUNDRY_PROJECT_ENDPOINT`.",
				Optional: true,
			},
			"api_key": schema.StringAttribute{
				MarkdownDescription: "An API key for the Foundry project. " +
					"Can also be set via `AZURE_AI_FOUNDRY_API_KEY`.",
				Optional:  true,
				Sensitive: true,
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Azure AD tenant ID. Reads `AZURE_TENANT_ID` or `ARM_TENANT_ID`.",
				Optional:            true,
			},
			"client_id": schema.StringAttribute{
				MarkdownDescription: "Service principal client ID. Reads `AZURE_CLIENT_ID` or `ARM_CLIENT_ID`.",
				Optional:            true,
			},
			"client_secret": schema.StringAttribute{
				MarkdownDescription: "Service principal client secret. Reads `AZURE_CLIENT_SECRET`.",
				Optional:            true,
				Sensitive:           true,
			},
			"oidc_token": schema.StringAttribute{
				MarkdownDescription: "OIDC client assertion / federated token. Used together with " +
					"`tenant_id` and `client_id` to authenticate via `ClientAssertionCredential`. " +
					"Reads `AZURE_OIDC_TOKEN` or `ARM_OIDC_TOKEN` (the latter for Pulumi ESC).",
				Optional:  true,
				Sensitive: true,
			},
			"use_azure_cli": schema.BoolAttribute{
				MarkdownDescription: "Use credentials from `az login`. Defaults to `false`.",
				Optional:            true,
			},
		},
	}
}

func (p *AzureFoundryProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	tflog.Info(ctx, "Configuring azurefoundry provider")

	var config AzureFoundryProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectEndpoint := firstNonEmpty(attrString(config.ProjectEndpoint), os.Getenv("AZURE_AI_FOUNDRY_PROJECT_ENDPOINT"))
	if projectEndpoint == "" {
		resp.Diagnostics.AddError(
			"Missing project_endpoint",
			"Set project_endpoint in the provider block or the AZURE_AI_FOUNDRY_PROJECT_ENDPOINT environment variable.",
		)
		return
	}

	auth := resolveAuth(config)
	if auth.err != nil {
		resp.Diagnostics.AddError("Failed to create "+auth.label+" credential", auth.err.Error())
		return
	}

	var apiClient *client.FoundryClient
	if auth.apiKey != "" {
		apiClient = client.NewFoundryClientWithAPIKey(projectEndpoint, auth.apiKey)
	} else {
		apiClient = client.NewFoundryClientWithCredential(projectEndpoint, auth.cred)
	}
	tflog.Info(ctx, "azurefoundry: authenticating with "+auth.label)

	resp.DataSourceData = apiClient
	resp.ResourceData = apiClient
}

// authResult carries the chosen auth method's output. Exactly one of apiKey or
// cred is set on success; err is set if credential construction failed.
type authResult struct {
	apiKey string
	cred   azcore.TokenCredential
	label  string
	err    error
}

// resolveAuth picks the first auth method whose inputs are present, in the
// fallback order documented in the README:
//  1. API key (config attr or AZURE_AI_FOUNDRY_API_KEY)
//  2. OIDC client assertion (tenant+client+oidc_token; checked before secret
//     so callers shipping both a fresh OIDC token and a stale CLIENT_SECRET in
//     env still get OIDC behavior)
//  3. Service principal with client secret
//  4. Azure CLI (use_azure_cli = true)
//  5. Default Azure credential chain (managed identity, workload identity, ...)
func resolveAuth(cfg AzureFoundryProviderModel) authResult {
	if k := firstNonEmpty(attrString(cfg.APIKey), os.Getenv("AZURE_AI_FOUNDRY_API_KEY")); k != "" {
		return authResult{apiKey: k, label: "API key"}
	}

	tenantID := firstNonEmpty(attrString(cfg.TenantID), os.Getenv("AZURE_TENANT_ID"), os.Getenv("ARM_TENANT_ID"))
	clientID := firstNonEmpty(attrString(cfg.ClientID), os.Getenv("AZURE_CLIENT_ID"), os.Getenv("ARM_CLIENT_ID"))

	if oidc := firstNonEmpty(attrString(cfg.OIDCToken), os.Getenv("AZURE_OIDC_TOKEN"), os.Getenv("ARM_OIDC_TOKEN")); tenantID != "" && clientID != "" && oidc != "" {
		cred, err := azidentity.NewClientAssertionCredential(
			tenantID, clientID,
			func(context.Context) (string, error) { return oidc, nil },
			nil,
		)
		return authResult{cred: cred, err: err, label: "OIDC client assertion"}
	}

	if secret := firstNonEmpty(attrString(cfg.ClientSecret), os.Getenv("AZURE_CLIENT_SECRET")); tenantID != "" && clientID != "" && secret != "" {
		cred, err := azidentity.NewClientSecretCredential(tenantID, clientID, secret, nil)
		return authResult{cred: cred, err: err, label: "service principal"}
	}

	if !cfg.UseAzureCLI.IsNull() && cfg.UseAzureCLI.ValueBool() {
		cred, err := azidentity.NewAzureCLICredential(nil)
		return authResult{cred: cred, err: err, label: "Azure CLI"}
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	return authResult{cred: cred, err: err, label: "default Azure credential chain"}
}

func (p *AzureFoundryProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resources.NewFoundryAgentResource,
		resources.NewFoundryFileResource,
		resources.NewFoundryVectorStoreResource,
		resources.NewFoundryAgentV2Resource,
		resources.NewFoundryFileV2Resource,
		resources.NewFoundryVectorStoreV2Resource,
		resources.NewFoundryMemoryStoreV2Resource,
	}
}

func (p *AzureFoundryProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func (p *AzureFoundryProvider) Functions(_ context.Context) []func() function.Function {
	return []func() function.Function{}
}

func attrString(attr types.String) string {
	if attr.IsNull() || attr.IsUnknown() {
		return ""
	}
	return attr.ValueString()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
