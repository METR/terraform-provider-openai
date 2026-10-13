package apikey

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openaiapi "github.com/openai/terraform-provider-openai/internal/provider/openaiapi"
)

var _ datasource.DataSource = &APIKeysDataSource{}
var _ datasource.DataSourceWithConfigure = &APIKeysDataSource{}

// APIKeysDataSource lists API keys: the organization's admin API keys, or one
// project's API keys. Only key metadata is read; the API never returns a key's
// secret value from a list.
type APIKeysDataSource struct {
	client    *openaiapi.APIClient
	typeName  string
	path      string
	byProject bool
}

type AdminAPIKeysDataSourceModel struct {
	ID      types.String `tfsdk:"id"`
	APIKeys types.List   `tfsdk:"api_keys"`
}

type ProjectAPIKeysDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	APIKeys   types.List   `tfsdk:"api_keys"`
}

type APIKeysDataSourceItemModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	CreatedAt  types.Int64  `tfsdk:"created_at"`
	LastUsedAt types.Int64  `tfsdk:"last_used_at"`
	OwnerType  types.String `tfsdk:"owner_type"`
	OwnerID    types.String `tfsdk:"owner_id"`
	OwnerName  types.String `tfsdk:"owner_name"`
}

func (APIKeysDataSourceItemModel) AttributeTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"id":           types.StringType,
		"name":         types.StringType,
		"created_at":   types.Int64Type,
		"last_used_at": types.Int64Type,
		"owner_type":   types.StringType,
		"owner_id":     types.StringType,
		"owner_name":   types.StringType,
	}
}

func NewAdminAPIKeysDataSource() datasource.DataSource {
	return &APIKeysDataSource{typeName: "openai_admin_api_keys", path: "/organization/admin_api_keys"}
}

func NewProjectAPIKeysDataSource() datasource.DataSource {
	return &APIKeysDataSource{typeName: "openai_project_api_keys", path: "/organization/projects/{project_id}/api_keys", byProject: true}
}

func (d *APIKeysDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = d.typeName
}

func (d *APIKeysDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	description := "List the organization's admin API keys."
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			MarkdownDescription: "Synthetic data source identifier.",
			Computed:            true,
		},
		"api_keys": schema.ListNestedAttribute{
			MarkdownDescription: "API keys returned by the API.",
			Computed:            true,
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"id":           schema.StringAttribute{MarkdownDescription: "id", Computed: true},
					"name":         schema.StringAttribute{MarkdownDescription: "name", Computed: true},
					"created_at":   schema.Int64Attribute{MarkdownDescription: "created at", Computed: true},
					"last_used_at": schema.Int64Attribute{MarkdownDescription: "last used at", Computed: true},
					"owner_type":   schema.StringAttribute{MarkdownDescription: "`user` or `service_account`", Computed: true},
					"owner_id":     schema.StringAttribute{MarkdownDescription: "owner id", Computed: true},
					"owner_name":   schema.StringAttribute{MarkdownDescription: "owner name", Computed: true},
				},
			},
		},
	}
	if d.byProject {
		description = "List a project's API keys, including its service accounts' keys."
		attributes["project_id"] = schema.StringAttribute{
			MarkdownDescription: "The project whose API keys to list.",
			Required:            true,
			Validators:          []validator.String{openaiapi.StringIsValidPathParameter()},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: description + "\n\nAuthentication: Set the admin_api_key provider attribute or OPENAI_ADMIN_KEY environment variable for the admin API audience.",
		Attributes:          attributes,
	}
}

func (d *APIKeysDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	providerData, ok := req.ProviderData.(*openaiapi.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type", fmt.Sprintf("Expected *openaiapi.ProviderData, got: %T", req.ProviderData))
		return
	}
	client, ok := providerData.Client("admin")
	if !ok {
		resp.Diagnostics.AddError(
			"Missing OpenAI credential for admin API",
			fmt.Sprintf("Set the admin_api_key provider attribute or OPENAI_ADMIN_KEY environment variable for the admin API audience before using %s. Credentials are not reused across API audiences.", d.typeName),
		)
		return
	}
	d.client = client
}

func (d *APIKeysDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError("Missing OpenAI API client", "The provider was not configured before this operation ran.")
		return
	}
	if !d.byProject {
		keys := d.list(ctx, map[string]string{}, resp)
		if resp.Diagnostics.HasError() {
			return
		}
		data := AdminAPIKeysDataSourceModel{ID: types.StringValue(d.typeName), APIKeys: keys}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}
	var data ProjectAPIKeysDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	keys := d.list(ctx, map[string]string{"project_id": data.ProjectID.ValueString()}, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	data.ID = data.ProjectID
	data.APIKeys = keys
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (d *APIKeysDataSource) list(ctx context.Context, pathParams map[string]string, resp *datasource.ReadResponse) types.List {
	none := types.ListNull(types.ObjectType{AttrTypes: APIKeysDataSourceItemModel{}.AttributeTypes()})
	responseData, err := d.client.PaginatedRequest(ctx, "GET", d.path, pathParams, map[string]string{"limit": "100"})
	if err != nil {
		resp.Diagnostics.AddError("OpenAI API request failed", err.Error())
		return none
	}
	resultItems, err := openaiapi.ResponseObjectList(responseData, "data", true)
	if err != nil {
		resp.Diagnostics.AddError("Invalid OpenAI API response", err.Error())
		return none
	}
	items := make([]APIKeysDataSourceItemModel, 0, len(resultItems))
	for _, itemData := range resultItems {
		item, err := apiKeyItem(itemData)
		if err != nil {
			resp.Diagnostics.AddError("Invalid OpenAI API response", err.Error())
			return none
		}
		items = append(items, item)
	}
	itemsValue, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: APIKeysDataSourceItemModel{}.AttributeTypes()}, items)
	resp.Diagnostics.Append(diags...)
	return itemsValue
}

// apiKeyItem reads one key. An admin API key's owner carries its id and name
// directly; a project API key nests them under owner.user or
// owner.service_account.
func apiKeyItem(itemData map[string]any) (APIKeysDataSourceItemModel, error) {
	item := APIKeysDataSourceItemModel{}
	for _, field := range []struct {
		paths    []string
		target   *types.String
		required bool
	}{
		{[]string{"id"}, &item.ID, true},
		{[]string{"name"}, &item.Name, false},
		{[]string{"owner.type"}, &item.OwnerType, false},
		{[]string{"owner.id", "owner.user.id", "owner.service_account.id"}, &item.OwnerID, false},
		{[]string{"owner.name", "owner.user.name", "owner.service_account.name"}, &item.OwnerName, false},
	} {
		if err := openaiapi.ApplyStringResponseField(itemData, field.paths, field.target, field.required); err != nil {
			return item, err
		}
	}
	if err := openaiapi.ApplyInt64ResponseField(itemData, []string{"created_at"}, &item.CreatedAt, false); err != nil {
		return item, err
	}
	if err := openaiapi.ApplyInt64ResponseField(itemData, []string{"last_used_at"}, &item.LastUsedAt, false); err != nil {
		return item, err
	}
	return item, nil
}
