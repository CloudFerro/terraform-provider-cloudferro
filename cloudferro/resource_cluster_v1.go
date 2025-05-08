package cloudferro

import (
	"context"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/int32validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"gitlab.cloudferro.com/k8s/api/clusterservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89abAB][0-9a-f]{3}-[0-9a-f]{12}$`)

var (
	_ resource.Resource                = (*clusterResource)(nil)
	_ resource.ResourceWithConfigure   = (*clusterResource)(nil)
	_ resource.ResourceWithImportState = (*clusterResource)(nil)
)

func newClusterResource() resource.Resource {
	return &clusterResource{}
}

type clusterModelControlPlane struct {
	Size          types.Int32  `tfsdk:"size"`
	MachineSpecID types.String `tfsdk:"machine_spec_id"`
}

type clusterModel struct {
	ID                  types.String             `tfsdk:"id"`
	Name                types.String             `tfsdk:"name"`
	Status              types.String             `tfsdk:"-"`
	KubernetesVersionID types.String             `tfsdk:"kubernetes_version_id"`
	ControlPlane        clusterModelControlPlane `tfsdk:"control_plane"`
	Kubeconfig          types.String             `tfsdk:"kubeconfig"`
	Metadata            types.Object             `tfsdk:"metadata"`
}

type clusterResource struct {
	cli *grpc.ClientConn
}

// ImportState implements resource.ResourceWithImportState.
func (c *clusterResource) ImportState(
	ctx context.Context,
	req resource.ImportStateRequest,
	resp *resource.ImportStateResponse,
) {
	var state clusterModel

	if req.ID == "" {
		resp.Diagnostics.AddError("failed to import state", "cluster_id is not known")
		return
	}

	if _, err := uuid.Parse(req.ID); err != nil {
		resp.Diagnostics.AddError("failed to import state", "cluster_id must be valid uuid")
	}

	state.ID = types.StringValue(req.ID)

	resp.Diagnostics.Append(refreshClusterState(ctx, c.cli, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Configure implements resource.ResourceWithConfigure.
func (c *clusterResource) Configure(
	ctx context.Context,
	req resource.ConfigureRequest,
	resp *resource.ConfigureResponse,
) {
	if req.ProviderData == nil {
		return
	}

	state, ok := req.ProviderData.(*providerState)
	if !ok {
		resp.Diagnostics.AddError("failed to configure resource", "invalid provider data type")
		return
	}
	c.cli = state.Cli
}

func refreshClusterState(ctx context.Context, cli *grpc.ClientConn, state *clusterModel) diag.Diagnostics {
	clusterID := state.ID.ValueString()
	var diags diag.Diagnostics

	clusterCli := clusterservice.NewClusterClient(cli)

	klaster, err := clusterCli.GetCluster(ctx, &clusterservice.GetClusterRequest{
		ClusterId: clusterID,
	})
	if err != nil {
		diags.AddError("failed to refresh cluster state", err.Error())
		return diags
	}

	state.ID = types.StringValue(klaster.GetId())
	state.Name = types.StringValue(klaster.GetName())
	state.Status = types.StringValue(klaster.GetStatus())

	if klaster.GetStatus() == "Running" {
		files, err := clusterCli.GetClusterFiles(ctx, &clusterservice.GetClusterFilesRequest{
			ClusterId: clusterID,
		})
		if err != nil {
			diags.AddError("failed to refresh cluster state", err.Error())
			return diags
		}

		state.Kubeconfig = types.StringValue(files.GetKubeconfig())
	}

	state.KubernetesVersionID = types.StringValue(klaster.GetVersion().GetId())
	state.ControlPlane.Size = types.Int32Value(klaster.GetControlPlane().GetCustom().GetSize())
	state.ControlPlane.MachineSpecID = types.StringValue(klaster.GetControlPlane().GetCustom().GetMachineSpec().GetId())

	if metadata := klaster.GetMetadata(); metadata != nil {
		obj, diag := types.ObjectValue(
			map[string]attr.Type{
				"openstack_project_id": types.StringType,
			},
			map[string]attr.Value{
				"openstack_project_id": types.StringValue(metadata.GetOpenstackProjectId()),
			})
		diags.Append(diag...)
		if diags.HasError() {
			return diags
		}

		state.Metadata = obj
	}

	return diags
}

// Create implements resource.Resource.
func (c *clusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterCli := clusterservice.NewClusterClient(c.cli)

	result, err := clusterCli.CreateCluster(ctx, &clusterservice.CreateClusterRequest{
		Cluster: &clusterservice.CreateCluster{
			Name: state.Name.ValueString(),
			KubernetesVersion: &clusterservice.CreateCluster_KubernetesVersion{
				Id: state.KubernetesVersionID.ValueString(),
			},
			ControlPlane: &clusterservice.CreateCluster_ControlPlane{
				Value: &clusterservice.CreateCluster_ControlPlane_Custom{
					Custom: &clusterservice.CreateCluster_ControlPlaneCustom{
						Size: state.ControlPlane.Size.ValueInt32(),
						MachineSpec: &clusterservice.CreateCluster_MachineSpec{
							Id: state.ControlPlane.MachineSpecID.ValueString(),
						},
					},
				},
			},
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster", err.Error())
		return
	}

	// update current state with id's
	state.ID = types.StringValue(result.Id)
	resp.Diagnostics.Append(refreshClusterState(ctx, c.cli, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("failed to create cluster", ctx.Err().Error())
			return

		case <-ticker.C:
			resp.Diagnostics.Append(refreshClusterState(ctx, c.cli, &state)...)
			if resp.Diagnostics.HasError() {
				return
			}

			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			if resp.Diagnostics.HasError() {
				return
			}

			if state.Status.ValueString() == "Error" {
				errStr := "Unknown error"
				resp.Diagnostics.AddError("failed to create cluster", errStr)
				return
			} else if state.Status.ValueString() == "Running" {
				break loop
			}

		}
	}
}

// Delete implements resource.Resource.
func (c *clusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsUnknown() {
		resp.Diagnostics.AddError("cluster id is not known", "huh")
		return
	}

	clusterID := state.ID.ValueString()

	cli := clusterservice.NewClusterClient(c.cli)

	cluster, err := cli.GetCluster(ctx, &clusterservice.GetClusterRequest{
		ClusterId: clusterID,
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to delete cluster", err.Error())
		return
	}

	if cluster.Status == "Running" {
		_, err = cli.DeleteCluster(ctx, &clusterservice.DeleteClusterRequest{
			ClusterId: clusterID,
		})
		if err != nil {
			resp.Diagnostics.AddError("failed to delete cluster", err.Error())
			return
		}

	} else if cluster.Status != "Deleting" {
		resp.Diagnostics.AddError("failed to delete cluster", "resource in the wrong state")
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {

		_, err = cli.GetCluster(ctx, &clusterservice.GetClusterRequest{
			ClusterId: clusterID,
		})
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return
		} else if err != nil {
			resp.Diagnostics.AddError("failed to delete cluster", err.Error())
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			continue
		}
	}
}

// Metadata implements resource.Resource.
func (c *clusterResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster_v1"
}

// Read implements resource.Resource.
func (c *clusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsUnknown() {
		resp.Diagnostics.AddError("failed to read cluster state", "cluster id is not known")
		return
	}

	resp.Diagnostics.Append(refreshClusterState(ctx, c.cli, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Schema implements resource.Resource.
func (c *clusterResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Id of the cluster.",
			},
			"kubernetes_version_id": schema.StringAttribute{
				Required:    true,
				Description: "Id of the kubernetes version.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(uuidRegex, "must be valid uuid"),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the cluster.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 64),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-_ ]*$`),
						"must start with character and contains only alphanumeric and -_ characters",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"control_plane": schema.SingleNestedAttribute{
				Required: true,
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
				Attributes: map[string]schema.Attribute{
					"size": schema.Int32Attribute{
						Required:    true,
						Description: "Size of the control plane.",
						Validators: []validator.Int32{
							int32validator.OneOf(1, 3, 5),
						},
					},
					"machine_spec_id": schema.StringAttribute{
						Required:    true,
						Description: "Id of the machine flavor.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(uuidRegex, "must be valid uuid"),
						},
					},
				},
			},
			"kubeconfig": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Cluster kubeconfig. Should be used with kubectl to interact with the cluster.",
			},
			"metadata": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "Cluster metadata.",
				Attributes: map[string]schema.Attribute{
					"openstack_project_id": schema.StringAttribute{
						Computed:    true,
						Description: "Id of the underlying OpenStack project where the cluster is created.",
					},
				},
			},
		},
	}
}

// Update implements resource.Resource.
func (c *clusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var current clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &current)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var request clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &request)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := current.ID.ValueString()

	cli := clusterservice.NewClusterClient(c.cli)

	klaster, err := cli.GetCluster(ctx, &clusterservice.GetClusterRequest{ClusterId: clusterID})
	if err != nil {
		resp.Diagnostics.AddError("failed to update cluster", err.Error())
		return
	}

	klaster.Version.Id = request.KubernetesVersionID.ValueString()

	tflog.Info(ctx, "update cluser", map[string]any{"object": klaster})
	_, err = cli.UpdateCluster(ctx, &clusterservice.UpdateClusterRequest{
		ClusterId: clusterID,
		Update:    klaster,
	})
	if err != nil {
		resp.Diagnostics.AddError("failde to update cluster", err.Error())
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		resp.Diagnostics.Append(refreshClusterState(ctx, c.cli, &current)...)
		if resp.Diagnostics.HasError() {
			return
		}

		resp.Diagnostics.Append(resp.State.Set(ctx, &current)...)
		if resp.Diagnostics.HasError() {
			return
		}

		if current.Status.ValueString() == "Running" {
			break
		}

		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("failed to update cluster", ctx.Err().Error())
		case <-ticker.C:
			continue
		}
	}
}
