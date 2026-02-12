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
	"gitlab.cloudferro.com/k8s/api/error/v1"
	"gitlab.cloudferro.com/k8s/api/kubernetesversionservice/v1"
	"gitlab.cloudferro.com/k8s/api/machinespecservice/v1"
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
	Size   types.Int32  `tfsdk:"size"`
	Flavor types.String `tfsdk:"flavor"`
}

type clusterModel struct {
	ID           types.String             `tfsdk:"id"`
	Name         types.String             `tfsdk:"name"`
	Status       types.String             `tfsdk:"-"`
	Version      types.String             `tfsdk:"version"`
	ControlPlane clusterModelControlPlane `tfsdk:"control_plane"`
	Kubeconfig   types.String             `tfsdk:"kubeconfig"`
	Metadata     types.Object             `tfsdk:"metadata"`
	RouterIP     types.String             `tfsdk:"router_ip"`
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

	resp.Diagnostics.Append(c.refreshClusterState(ctx, &state)...)
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

func (c *clusterResource) refreshClusterState(ctx context.Context, state *clusterModel) diag.Diagnostics {
	clusterID := state.ID.ValueString()
	var diags diag.Diagnostics

	clusterCli := clusterservice.NewClusterClient(c.cli)

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
	state.Version = types.StringValue(klaster.GetVersion().GetVersion())
	state.ControlPlane.Size = types.Int32Value(klaster.GetControlPlane().GetCustom().GetSize())
	state.ControlPlane.Flavor = types.StringValue(klaster.GetControlPlane().GetCustom().GetMachineSpec().GetName())
	if klaster.GetRouterIp() != "" {
		state.RouterIP = types.StringValue(klaster.GetRouterIp())
	} else if state.RouterIP.IsUnknown() {
		state.RouterIP = types.StringNull()
	}

	if klaster.GetStatus() == "Running" {
		files, err := clusterCli.GetClusterFiles(ctx, &clusterservice.GetClusterFilesRequest{
			ClusterId: clusterID,
		})
		if err != nil {
			diags.AddError("failed to refresh cluster state", err.Error())
			return diags
		}

		state.Kubeconfig = types.StringValue(files.GetKubeconfig())
	} else if state.Kubeconfig.IsUnknown() {
		state.Kubeconfig = types.StringNull()
	}

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
	} else if state.Metadata.IsUnknown() {
		state.Metadata = types.ObjectNull(
			map[string]attr.Type{
				"openstack_project_id": types.StringType,
			},
		)
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
	versionCli := kubernetesversionservice.NewKubernetesVersionClient(c.cli)
	machineCli := machinespecservice.NewMachineSpecClient(c.cli)

	machines, err := machineCli.List(ctx, &machinespecservice.ListRequest{
		Name: state.ControlPlane.Flavor.ValueStringPointer(),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster", err.Error())
		return
	}

	if len(machines.GetItems()) != 1 {
		resp.Diagnostics.AddError("failed to create cluster", "failed to find flavor")
		return
	}

	versions, err := versionCli.List(ctx, &kubernetesversionservice.ListRequest{
		Version: state.Version.ValueStringPointer(),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster", err.Error())
		return
	}

	if len(versions.GetItems()) != 1 {
		resp.Diagnostics.AddError("failed to create cluster", "failed to find kubernetes version")
		return
	}

	result, err := clusterCli.CreateCluster(ctx, &clusterservice.CreateClusterRequest{
		Cluster: &clusterservice.CreateCluster{
			Name: state.Name.ValueString(),
			KubernetesVersion: &clusterservice.CreateCluster_KubernetesVersion{
				Id: versions.GetItems()[0].GetId(),
			},
			ControlPlane: &clusterservice.CreateCluster_ControlPlane{
				Value: &clusterservice.CreateCluster_ControlPlane_Custom{
					Custom: &clusterservice.CreateCluster_ControlPlaneCustom{
						Size: state.ControlPlane.Size.ValueInt32(),
						MachineSpec: &clusterservice.CreateCluster_MachineSpec{
							Id: machines.GetItems()[0].GetId(),
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
	resp.Diagnostics.Append(c.refreshClusterState(ctx, &state)...)
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
			resp.Diagnostics.Append(c.refreshClusterState(ctx, &state)...)
			if resp.Diagnostics.HasError() {
				return
			}

			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			if resp.Diagnostics.HasError() {
				return
			}

			if state.Status.ValueString() == "Error" {
				cli := clusterservice.NewClusterClient(c.cli)

				r, err := cli.GetCluster(ctx, &clusterservice.GetClusterRequest{
					ClusterId:   state.ID.ValueString(),
					ExtraFields: "errors",
				})
				if err != nil {
					resp.Diagnostics.AddError("failed to create cluster", err.Error())
					return
				}

				latesrErr := &error.Error{}
				for _, er := range r.GetErrors() {
					if latesrErr.CreatedAt.AsTime().Before(er.GetCreatedAt().AsTime()) {
						latesrErr = er
					}
				}

				errStr := "Unknown error"
				if !latesrErr.CreatedAt.AsTime().IsZero() {
					errStr = latesrErr.GetMsg()
				}

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

	if cluster.Status == "Running" || cluster.Status == "Error" {
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

	resp.Diagnostics.Append(c.refreshClusterState(ctx, &state)...)
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
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"version": schema.StringAttribute{
				Required:    true,
				Description: "Kubernetes version.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`\d+\.\d+\.\d+`), "must be a valid version"),
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
					"flavor": schema.StringAttribute{
						Required:    true,
						Description: "Machine flavor to use for control plane.",
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
			"router_ip": schema.StringAttribute{
				Computed:    true,
				Description: "Address of the cluster gateway.",
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
	versionCli := kubernetesversionservice.NewKubernetesVersionClient(c.cli)

	klaster, err := cli.GetCluster(ctx, &clusterservice.GetClusterRequest{ClusterId: clusterID})
	if err != nil {
		resp.Diagnostics.AddError("failed to update cluster", err.Error())
		return
	}

	xTrue := true
	versions, err := versionCli.List(ctx, &kubernetesversionservice.ListRequest{
		Version:  request.Version.ValueStringPointer(),
		IsActive: &xTrue,
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to update cluster", err.Error())
		return
	}

	if len(versions.GetItems()) != 1 {
		resp.Diagnostics.AddError(
			"failed to update cluster",
			"failed to get kubernetes version",
		)
		return
	}

	klaster.Version.Id = versions.GetItems()[0].GetId()

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
		resp.Diagnostics.Append(c.refreshClusterState(ctx, &current)...)
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

		if current.Status.ValueString() == "Error" {
			resp.Diagnostics.AddError(
				"failed to update cluster",
				"cluster in the 'Error' status.",
			)
			return
		}

		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("failed to update cluster", ctx.Err().Error())
		case <-ticker.C:
			continue
		}
	}
}
