package cloudferro

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/cloudferro/terraform-provider-cloudferro/internal/kubeconfig"
	"github.com/cloudferro/terraform-provider-cloudferro/internal/utils"
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
	"gitlab.cloudferro.com/k8s/api/clusterservice/v1"
	"gitlab.cloudferro.com/k8s/api/kubernetesversion/v1"
	"gitlab.cloudferro.com/k8s/api/kubernetesversionservice/v1"
	"gitlab.cloudferro.com/k8s/api/machinespec/v1"
	"gitlab.cloudferro.com/k8s/api/machinespecservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	resourceMutexes sync.Map
	globalMutex     sync.Mutex
)

func getResourceMutex(resourceID string) *sync.Mutex {
	globalMutex.Lock()
	defer globalMutex.Unlock()

	actual, _ := resourceMutexes.LoadOrStore(resourceID, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

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

type clusterKubeconfig struct {
	Raw           types.String `tfsdk:"raw"`
	Host          types.String `tfsdk:"host"`
	ClientCert    types.String `tfsdk:"client_certificate"`
	ClientKey     types.String `tfsdk:"client_key"`
	ClusterCaCert types.String `tfsdk:"cluster_ca_certificate"`
}

var kubeconfigType = map[string]attr.Type{
	"raw":                    types.StringType,
	"host":                   types.StringType,
	"client_certificate":     types.StringType,
	"client_key":             types.StringType,
	"cluster_ca_certificate": types.StringType,
}

type clusterModel struct {
	ID           types.String             `tfsdk:"id"`
	Name         types.String             `tfsdk:"name"`
	Status       types.String             `tfsdk:"-"`
	Version      types.String             `tfsdk:"version"`
	ControlPlane clusterModelControlPlane `tfsdk:"control_plane"`
	Kubeconfig   types.Object             `tfsdk:"kubeconfig"`
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
		return
	}

	state.ID = types.StringValue(req.ID)

	resp.Diagnostics.Append(c.refreshClusterState(ctx, &state, false)...)
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

func (c *clusterResource) refreshClusterState(ctx context.Context, state *clusterModel, loadErrors bool) diag.Diagnostics {
	clusterID := state.ID.ValueString()
	var diags diag.Diagnostics

	clusterCli := clusterservice.NewClusterClient(c.cli)

	klaster, err := clusterCli.GetCluster(ctx, &clusterservice.GetClusterRequest{
		ClusterId:   clusterID,
		ExtraFields: "errors",
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

	if state.Kubeconfig.IsUnknown() {
		state.Kubeconfig = types.ObjectNull(kubeconfigType)
	}

	if klaster.GetStatus() == "Running" {
		if state.Kubeconfig.IsNull() {
			files, err := clusterCli.GetClusterFiles(ctx, &clusterservice.GetClusterFilesRequest{
				ClusterId: clusterID,
			})
			if err != nil {
				diags.AddError("failed to refresh cluster state", err.Error())
				return diags
			}

			config, err := kubeconfigFromConfig(files.GetKubeconfig())
			if err != nil {
				diags.AddError("failed to refresh cluster state", err.Error())
				return diags
			}

			var configDiags diag.Diagnostics
			state.Kubeconfig, configDiags = types.ObjectValueFrom(ctx, kubeconfigType, config)
			diags.Append(configDiags...)
			if diags.HasError() {
				return diags
			}

		}
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

	if klaster.Status == "Error" && loadErrors {
		if len(klaster.Errors) > 0 {
			lastErr, err := utils.GetLatestClusterError(ctx, c.cli, clusterID)
			if err != nil {
				diags.AddError("failed to refresh cluster state", err.Error())
				return diags
			}

			diags.AddError("failed to refresh cluster state", lastErr.Msg)
		} else {
			diags.AddError("failed to refresh cluster state", "unknown error")
		}
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

	var machineSpec *machinespec.MachineSpec
	machines, err := machineCli.List(ctx, &machinespecservice.ListRequest{
		Name: state.ControlPlane.Flavor.ValueStringPointer(),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster", err.Error())
		return
	}

	for _, el := range machines.GetItems() {
		if el.GetName() == state.ControlPlane.Flavor.ValueString() {
			machineSpec = el
			break
		}
	}
	if machineSpec == nil {
		resp.Diagnostics.AddError("failed to create cluster", "control plane flavor not found")
		return
	}

	var version *kubernetesversion.KubernetesVersion
	versions, err := versionCli.List(ctx, &kubernetesversionservice.ListRequest{
		Version: state.Version.ValueStringPointer(),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster", err.Error())
		return
	}

	for _, el := range versions.GetItems() {
		if el.GetVersion() == state.Version.ValueString() {
			version = el
			break
		}
	}

	if version == nil {
		resp.Diagnostics.AddError("failed to create cluster", "version not found")
		return
	}

	result, err := clusterCli.CreateCluster(ctx, &clusterservice.CreateClusterRequest{
		Cluster: &clusterservice.CreateCluster{
			Name: state.Name.ValueString(),
			KubernetesVersion: &clusterservice.CreateCluster_KubernetesVersion{
				Id: version.Id,
			},
			ControlPlane: &clusterservice.CreateCluster_ControlPlane{
				Value: &clusterservice.CreateCluster_ControlPlane_Custom{
					Custom: &clusterservice.CreateCluster_ControlPlaneCustom{
						Size: state.ControlPlane.Size.ValueInt32(),
						MachineSpec: &clusterservice.CreateCluster_MachineSpec{
							Id: machineSpec.Id,
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
	resp.Diagnostics.Append(c.refreshClusterState(ctx, &state, true)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		resp.Diagnostics.Append(c.refreshClusterState(ctx, &state, true)...)
		if resp.Diagnostics.HasError() {
			return
		}

		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}

		if state.Status.ValueString() == "Running" {
			return
		}
		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("failed to create cluster", ctx.Err().Error())
			return

		case <-ticker.C:
			continue

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

	var err error
	_, err = cli.DeleteCluster(ctx, &clusterservice.DeleteClusterRequest{
		ClusterId: state.ID.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to delete cluster", err.Error())
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		cl, err := cli.GetCluster(ctx, &clusterservice.GetClusterRequest{
			ClusterId: clusterID,
		})
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return
		} else if err != nil {
			resp.Diagnostics.AddError("failed to delete cluster", err.Error())
			return
		}

		if cl.Status == "Error" {
			lastErr, err := utils.GetLatestClusterError(ctx, c.cli, clusterID)
			if err != nil {
				resp.Diagnostics.AddError("failed to delete cluster", err.Error())
				return
			}

			if lastErr != nil {
				resp.Diagnostics.AddError("failed to delete cluster", lastErr.Msg)
			} else {
				resp.Diagnostics.AddError("failed to delete cluster", "cluster in the invalid state")
			}
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

	resp.Diagnostics.Append(c.refreshClusterState(ctx, &state, false)...)
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
		Version: 1,
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
			"kubeconfig": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "Cluster kubeconfig. Should be used with kubectl to interact with the cluster.",
				Attributes: map[string]schema.Attribute{
					"raw": schema.StringAttribute{
						Computed:    true,
						Sensitive:   true,
						Description: "Raw kubeconfig. Should be used with kubectl to interact with the cluster.",
					},
					"host": schema.StringAttribute{
						Computed:    true,
						Description: "The host of the kubernetes cluster.",
					},
					"client_certificate": schema.StringAttribute{
						Computed:    true,
						Description: "The client certificate for the kubernetes cluster.",
					},
					"client_key": schema.StringAttribute{
						Computed:    true,
						Sensitive:   true,
						Description: "The client key for the kubernetes cluster.",
					},
					"cluster_ca_certificate": schema.StringAttribute{
						Computed:    true,
						Description: "The cluster CA certificate for the kubernetes cluster.",
					},
				},
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
		resp.Diagnostics.Append(c.refreshClusterState(ctx, &current, true)...)
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
			return
		case <-ticker.C:
			continue
		}
	}
}

func kubeconfigFromConfig(raw string) (*clusterKubeconfig, error) {
	config, err := kubeconfig.Parse(raw)
	if err != nil {
		return nil, err
	}

	currentContext := config.CurrentContext
	if currentContext == "" {
		for _, ctx := range config.Contexts {
			currentContext = ctx.Name
			break
		}
	}

	var context *kubeconfig.Context
	for _, ctx := range config.Contexts {
		if ctx.Name == currentContext {
			context = &ctx.Context
			break
		}
	}

	if context == nil {
		return &clusterKubeconfig{}, nil
	}

	var cluster *kubeconfig.Cluster
	var user *kubeconfig.User

	for _, cl := range config.Clusters {
		if cl.Name == context.Cluster {
			cluster = &cl.Cluster
			break
		}
	}

	for _, au := range config.AuthInfos {
		if au.Name == context.User {
			user = &au.User
		}
	}

	if cluster == nil || user == nil {
		return &clusterKubeconfig{}, nil
	}

	return &clusterKubeconfig{
		Raw:           types.StringValue(raw),
		Host:          types.StringValue(cluster.Server),
		ClientCert:    types.StringValue(user.ClientCertificateData),
		ClientKey:     types.StringValue(user.ClientKeyData),
		ClusterCaCert: types.StringValue(cluster.CertificateAuthorityData),
	}, nil
}
