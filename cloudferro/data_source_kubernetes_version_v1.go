package cloudferro

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"gitlab.cloudferro.com/k8s/api/kubernetesversion/v1"
	"gitlab.cloudferro.com/k8s/api/kubernetesversionservice/v1"
	"google.golang.org/grpc"
)

var (
	_ datasource.DataSource              = (*kubernetesVersionDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*kubernetesVersionDataSource)(nil)
)

func newKubernetesVersionDataSource() datasource.DataSource {
	return &kubernetesVersionDataSource{}
}

type kubernetesVersionDataSource struct {
	cli *grpc.ClientConn
}

// Configure implements datasource.DataSourceWithConfigure.
func (m *kubernetesVersionDataSource) Configure(
	c context.Context,
	req datasource.ConfigureRequest,
	resp *datasource.ConfigureResponse,
) {
	if req.ProviderData == nil {
		return
	}

	state, ok := req.ProviderData.(*providerState)
	if !ok {
		resp.Diagnostics.AddError("failed to configure data source", "invalid provider data")
	}

	m.cli = state.Cli
}

// Metadata implements datasource.DataSource.
func (m *kubernetesVersionDataSource) Metadata(
	c context.Context,
	req datasource.MetadataRequest,
	resp *datasource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_version_v1"
}

// Read implements datasource.DataSource.
func (m *kubernetesVersionDataSource) Read(
	c context.Context,
	req datasource.ReadRequest,
	resp *datasource.ReadResponse,
) {
	var state kubernetesVersionModel
	resp.Diagnostics.Append(req.Config.Get(c, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	version := state.Version.ValueString()

	if id == "" && version == "" {
		resp.Diagnostics.AddError("invalid data", "id or version must be provided")
		return
	}
	var result *kubernetesversion.KubernetesVersion

	if id != "" {

		cli := kubernetesversionservice.NewKubernetesVersionClient(m.cli)

		xTrue := true
		items, err := cli.List(c, &kubernetesversionservice.ListRequest{
			IsActive: &xTrue,
		})
		if err != nil {
			resp.Diagnostics.AddError("failed to get data", err.Error())
			return
		}

		for _, it := range items.Items {
			if it.Id == id {
				result = it
				break
			}
		}
		if result == nil {
			resp.Diagnostics.AddError("failed to get data", "not found")
			return
		}
	} else {
		cli := kubernetesversionservice.NewKubernetesVersionClient(m.cli)

		xTrue := true
		items, err := cli.List(c, &kubernetesversionservice.ListRequest{
			Version:  state.Version.ValueStringPointer(),
			IsActive: &xTrue,
		})
		if err != nil {
			resp.Diagnostics.AddError("failed to get data", err.Error())
			return
		}

		if len(items.Items) != 1 {
			resp.Diagnostics.AddError("failed to get data", "invalid number of items found")
			return
		}

		result = items.Items[0]
	}

	state.ID = types.StringValue(result.Id)
	state.Version = types.StringValue(result.Version)
	state.Info = types.StringValue(result.Info)
	state.EndOfLife = types.StringValue(result.Eol.AsTime().String())

	resp.Diagnostics.Append(resp.State.Set(c, &state)...)
}

type kubernetesVersionModel struct {
	ID        types.String `tfsdk:"id"`
	Version   types.String `tfsdk:"version"`
	EndOfLife types.String `tfsdk:"end_of_life"`
	Info      types.String `tfsdk:"info"`
}

// Schema implements datasource.DataSource.
func (m *kubernetesVersionDataSource) Schema(
	c context.Context,
	req datasource.SchemaRequest,
	resp *datasource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Optional: true,
				Description: "Internal version id.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(uuidRegex, "must be valid uuid"),
				},
			},
			"version": schema.StringAttribute{
				Computed: true, Optional: true,
				Description: "Kubernetes version.",
			},
			"end_of_life": schema.StringAttribute{
				Computed:    true,
				Description: "End of life date for the version. After this time the version shouldn't be used.",
			},
			"info": schema.StringAttribute{
				Computed:    true,
				Description: "Additional information about the version.",
			},
		},
	}
}
