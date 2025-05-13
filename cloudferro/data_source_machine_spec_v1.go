package cloudferro

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"gitlab.cloudferro.com/k8s/api/machinespec/v1"
	"gitlab.cloudferro.com/k8s/api/machinespecservice/v1"
	"google.golang.org/grpc"
)

var (
	_ datasource.DataSource              = (*machineSpecDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*machineSpecDataSource)(nil)
)

func newMachineSpecDataSource() datasource.DataSource {
	return &machineSpecDataSource{}
}

type machineSpecDataSource struct {
	cli *grpc.ClientConn
}

// Configure implements datasource.DataSourceWithConfigure.
func (m *machineSpecDataSource) Configure(
	c context.Context,
	req datasource.ConfigureRequest,
	resp *datasource.ConfigureResponse,
) {
	if req.ProviderData == nil {
		return
	}

	state, ok := req.ProviderData.(*providerState)
	if !ok {
		resp.Diagnostics.AddError("unexpected data source configuration data", "expected grpc.ClientConn")
		return
	}

	m.cli = state.Cli
}

// Metadata implements datasource.DataSource.
func (m *machineSpecDataSource) Metadata(
	c context.Context,
	req datasource.MetadataRequest,
	resp *datasource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_machine_spec_v1"
}

// Read implements datasource.DataSource.
func (m *machineSpecDataSource) Read(c context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state machineSpecModel
	resp.Diagnostics.Append(req.Config.Get(c, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	name := state.Name.ValueString()
	region := state.Region.ValueString()

	// fix the check
	// must provide either id and no name/region
	// or no id and name+region

	var result *machinespec.MachineSpec
	if id != "" {
		if name != "" || region != "" {
			resp.Diagnostics.AddWarning("failed to read machine spec", "provided name or region with id, using id")
		}

		cli := machinespecservice.NewMachineSpecClient(m.cli)

		specs, err := cli.List(c, &machinespecservice.ListRequest{})
		if err != nil {
			resp.Diagnostics.AddError(
				"failed to read machine spec",
				fmt.Sprintf("failed to get machine specs: %v", err),
			)
			return
		}

		for _, sp := range specs.Items {
			if sp.Id == id {
				result = sp
				break
			}
		}
		if result == nil {
			resp.Diagnostics.AddError(
				"failed to read machine spec",
				fmt.Sprintf("machine spec with id = %s not found", state.ID),
			)
			return
		}

	} else {
		if name == "" || region == "" {
			resp.Diagnostics.AddError("failed to read machine spec", "name or region is missing")
			return
		}

		cli := machinespecservice.NewMachineSpecClient(m.cli)

		specs, err := cli.List(c, &machinespecservice.ListRequest{
			Region: state.Region.ValueStringPointer(),
			Name:   state.Name.ValueStringPointer(),
		})
		if err != nil {
			resp.Diagnostics.AddError("failed to read machine spec", fmt.Sprintf("failed to get machine specs: %v", err))
			return
		}

		if len(specs.Items) != 1 {
			resp.Diagnostics.AddError(
				"failed to read machine spec",
				fmt.Sprintf("machine spec with name = %s and region = %s not found", state.Name, state.Region),
			)
		}

		result = specs.Items[0]
	}

	state.ID = types.StringValue(result.Id)
	state.Name = types.StringValue(result.Name)
	state.Region = types.StringValue(result.Region)
	state.CPU = types.Int32Value(result.Cpu)
	state.Memory = types.Int64Value(result.Memory)
	state.LocalDiskSize = types.Int64Value(result.LocalDiskSize)

	resp.Diagnostics.Append(resp.State.Set(c, state)...)
}

type machineSpecModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Region        types.String `tfsdk:"region"`
	CPU           types.Int32  `tfsdk:"cpu"`
	Memory        types.Int64  `tfsdk:"memory"`
	LocalDiskSize types.Int64  `tfsdk:"local_disk_size"`
}

// Schema implements datasource.DataSource.
func (m *machineSpecDataSource) Schema(
	c context.Context,
	req datasource.SchemaRequest,
	resp *datasource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Optional: true,
				Description: "Internal of of the machine specification.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(uuidRegex, "must be valid uuid"),
				},
			},
			"name": schema.StringAttribute{
				Computed: true, Optional: true,
				Description: "Name of the machine specification/flavor.",
			},
			"region": schema.StringAttribute{
				Computed: true, Optional: true,
				Description: "Region name.",
			},
			"cpu": schema.Int32Attribute{
				Computed:    true,
				Description: "Number of CPU cores.",
			},
			"memory": schema.Int64Attribute{
				Computed:    true,
				Description: "Size of the available RAM in MB.",
			},
			"local_disk_size": schema.Int64Attribute{
				Computed:    true,
				Description: "Size of the local disk in GB.",
			},
		},
	}
}
