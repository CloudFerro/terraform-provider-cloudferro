package cloudferro

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/int32validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"gitlab.cloudferro.com/k8s/api/machinespecservice/v1"
	"gitlab.cloudferro.com/k8s/api/nodepool/v1"
	"gitlab.cloudferro.com/k8s/api/nodepoolservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	_ resource.Resource                     = (*nodePoolResource)(nil)
	_ resource.ResourceWithConfigure        = (*nodePoolResource)(nil)
	_ resource.ResourceWithImportState      = (*nodePoolResource)(nil)
	_ resource.ResourceWithConfigValidators = (*nodePoolResource)(nil)
)

func newNodePoolResource() resource.Resource {
	return &nodePoolResource{}
}

type labelModel struct {
	Key   types.String `tfsdk:"key"`
	Value types.String `tfsdk:"value"`
}

type taintModel struct {
	Key    types.String `tfsdk:"key"`
	Value  types.String `tfsdk:"value"`
	Effect types.String `tfsdk:"effect"`
}

type nodePoolModel struct {
	ClusterID      types.String `tfsdk:"cluster_id"`
	ID             types.String `tfsdk:"id"`
	Status         types.String `tfsdk:"-"`
	Name           types.String `tfsdk:"name"`
	Flavor         types.String `tfsdk:"flavor"`
	Autoscale      types.Bool   `tfsdk:"autoscale"`
	Size           types.Int32  `tfsdk:"size"`
	SizeMin        types.Int32  `tfsdk:"size_min"`
	SizeMax        types.Int32  `tfsdk:"size_max"`
	SharedNetworks types.List   `tfsdk:"shared_networks"`
	Taints         types.List   `tfsdk:"taints"`
	Labels         types.List   `tfsdk:"labels"`
}

type nodePoolResource struct {
	cli *grpc.ClientConn
}

// ConfigValidators implements resource.ResourceWithConfigValidators.
func (c *nodePoolResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.AtLeastOneOf(
			path.MatchRoot("size"),
			path.MatchRoot("size_min"),
			path.MatchRoot("size_max"),
		),
	}
}

// ImportState implements resource.ResourceWithImportState.
func (c *nodePoolResource) ImportState(
	ctx context.Context,
	req resource.ImportStateRequest,
	resp *resource.ImportStateResponse,
) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"failed to import node pool state",
			"id must be in the format of <cluster_id>/<node_pool_id>",
		)
		return
	}

	var err error

	if _, err = uuid.Parse(parts[0]); err != nil {
		resp.Diagnostics.AddError("failed to import node pool state", err.Error())
		return
	}

	if _, err = uuid.Parse(parts[1]); err != nil {
		resp.Diagnostics.AddError("failed to import node pool state", err.Error())
		return
	}

	var state nodePoolModel

	state.ClusterID = types.StringValue(parts[0])
	state.ID = types.StringValue(parts[1])

	resp.Diagnostics.Append(refreshNodePoolState(ctx, c.cli, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Configure implements resource.ResourceWithConfigure.
func (c *nodePoolResource) Configure(
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

func refreshNodePoolState(ctx context.Context, cli *grpc.ClientConn, state *nodePoolModel) diag.Diagnostics {
	var diags diag.Diagnostics
	clusterID := state.ClusterID.ValueString()
	nodePoolID := state.ID.ValueString()

	nodePoolCli := nodepoolservice.NewNodePoolClient(cli)

	tflog.Debug(ctx, "refresh state, getting node pool")
	nodePool, err := nodePoolCli.GetNodePool(ctx, &nodepoolservice.GetNodePoolRequest{
		ClusterId:  clusterID,
		NodePoolId: nodePoolID,
	})
	if err != nil {
		diags.AddError("failed to refresh node pool state", err.Error())
		return diags
	}

	state.Status = types.StringValue(nodePool.GetStatus())
	state.Flavor = types.StringValue(nodePool.GetMachineSpec().GetName())
	state.Name = types.StringValue(nodePool.GetName())
	state.Autoscale = types.BoolValue(nodePool.GetAutoscale())
	state.Size = types.Int32PointerValue(nodePool.Size)
	state.SizeMax = types.Int32PointerValue(nodePool.SizeMax)
	state.SizeMin = types.Int32PointerValue(nodePool.SizeMin)

	var sharedNetworks []attr.Value
	for _, el := range nodePool.SharedNetworks {
		sharedNetworks = append(sharedNetworks, types.StringValue(el))
	}
	if state.SharedNetworks.IsUnknown() || state.SharedNetworks.IsNull() {
		state.SharedNetworks = types.ListNull(types.StringType)
	} else {
		state.SharedNetworks = types.ListValueMust(types.StringType, sharedNetworks)
	}

	labelsInnerType := map[string]attr.Type{
		"key":   types.StringType,
		"value": types.StringType,
	}

	tflog.Debug(ctx, "refresh state, parsing labels")
	var labels []attr.Value
	for _, el := range nodePool.Labels {

		obj, diag := types.ObjectValue(
			labelsInnerType,
			map[string]attr.Value{
				"key":   types.StringValue(el.Key),
				"value": types.StringValue(el.Value),
			},
		)
		diags.Append(diag...)
		if diags.HasError() {
			return diags
		}

		labels = append(labels, obj)

	}

	if state.Labels.IsUnknown() || state.Labels.IsNull() {
		state.Labels = types.ListNull(types.ObjectType{AttrTypes: labelsInnerType})
	} else {
		state.Labels = types.ListValueMust(types.ObjectType{AttrTypes: labelsInnerType}, labels)
	}

	taintsInnerType := map[string]attr.Type{
		"key":    types.StringType,
		"value":  types.StringType,
		"effect": types.StringType,
	}

	tflog.Debug(ctx, "refresh state, parsing taints")
	var taints []attr.Value
	for _, el := range nodePool.Taints {
		effect, ok := nodepool.Taint_Effect_name[int32(el.Effect)]
		if !ok {
			diags.AddError("failed to refresh node pool state", "invalid effect value")
			return diags
		}

		obj, diag := types.ObjectValue(
			taintsInnerType,
			map[string]attr.Value{
				"key":    types.StringValue(el.Key),
				"value":  types.StringValue(el.Value),
				"effect": types.StringValue(effect),
			},
		)
		diags.Append(diag...)
		if diags.HasError() {
			return diags
		}

		taints = append(taints, obj)

	}

	if state.Taints.IsUnknown() || state.Taints.IsNull() {
		state.Taints = types.ListNull(types.ObjectType{AttrTypes: taintsInnerType})
	} else {
		state.Taints = types.ListValueMust(types.ObjectType{AttrTypes: taintsInnerType}, taints)
	}

	return diags
}

// Create implements resource.Resource.
func (c *nodePoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state nodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := state.ClusterID.ValueString()

	npCli := nodepoolservice.NewNodePoolClient(c.cli)
	msCli := machinespecservice.NewMachineSpecClient(c.cli)

	var sharedNetworks []string
	var labels []*nodepool.Label
	var taints []*nodepool.Taint
	if !state.SharedNetworks.IsNull() && !state.SharedNetworks.IsUnknown() {
		var sharedNetworksElems []types.String
		resp.Diagnostics.Append(state.SharedNetworks.ElementsAs(ctx, &sharedNetworksElems, false)...)
		if resp.Diagnostics.HasError() {
			return
		}

		for _, el := range sharedNetworksElems {
			sharedNetworks = append(sharedNetworks, el.ValueString())
		}
	}

	tflog.Debug(ctx, "parsing labels")
	if !state.Labels.IsNull() && !state.Labels.IsUnknown() {
		var labelsElems []labelModel
		resp.Diagnostics.Append(state.Labels.ElementsAs(ctx, &labelsElems, false)...)
		if resp.Diagnostics.HasError() {
			return
		}

		for _, el := range labelsElems {
			labels = append(labels, &nodepool.Label{
				Key:   el.Key.ValueString(),
				Value: el.Value.ValueString(),
			})
		}
	}

	tflog.Debug(ctx, "parsing taints")
	if !state.Taints.IsNull() && !state.Taints.IsUnknown() {
		var taintsElems []taintModel
		tflog.Debug(ctx, "parsing taint elements")
		resp.Diagnostics.Append(state.Taints.ElementsAs(ctx, &taintsElems, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		tflog.Debug(ctx, "done parsing taint elements")

		for _, el := range taintsElems {
			effect := nodepool.Taint_Effect_value[el.Effect.ValueString()]
			taints = append(taints, &nodepool.Taint{
				Key:    el.Key.ValueString(),
				Value:  el.Value.ValueString(),
				Effect: nodepool.Taint_Effect(effect),
			})
		}
	}

	machineSpecs, err := msCli.List(ctx, &machinespecservice.ListRequest{
		Name: state.Flavor.ValueStringPointer(),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create node pool", err.Error())
		return
	}

	if len(machineSpecs.Items) != 1 {
		resp.Diagnostics.AddError("failde to create node pool", "flavor not found")
		return
	}

	result, err := npCli.CreateNodePool(ctx, &nodepoolservice.CreateNodePoolRequest{
		ClusterId: clusterID,
		NodePool: &nodepoolservice.NodePoolCreate{
			MachineSpec: &nodepoolservice.NodePoolCreate_MachineSpec{
				Id: machineSpecs.GetItems()[0].GetId(),
			},
			Name:           state.Name.ValueStringPointer(),
			Size:           state.Size.ValueInt32Pointer(),
			SizeMin:        state.SizeMin.ValueInt32Pointer(),
			SizeMax:        state.SizeMax.ValueInt32Pointer(),
			Autoscale:      state.Autoscale.ValueBool(),
			SharedNetworks: sharedNetworks,
			Labels:         labels,
			Taints:         taints,
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create node pool", err.Error())
		return
	}

	// update current state with id's
	state.ID = types.StringValue(result.Id)
	resp.Diagnostics.Append(refreshNodePoolState(ctx, c.cli, &state)...)
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
		resp.Diagnostics.Append(refreshNodePoolState(ctx, c.cli, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}

		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}

		if state.Status.ValueString() == "Error" {
			errStr := "Unknown error"
			resp.Diagnostics.AddError("failed to create node pool", errStr)
			return
		} else if state.Status.ValueString() == "Running" {
			break
		}

		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("failed to create node pool", ctx.Err().Error())
			return

		case <-ticker.C:
			continue
		}
	}
}

// Delete implements resource.Resource.
func (c *nodePoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state nodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsUnknown() {
		resp.Diagnostics.AddError("node pool id is not known", "huh")
		return
	}

	nodePoolID := state.ID.ValueString()
	clusterID := state.ClusterID.ValueString()

	cli := nodepoolservice.NewNodePoolClient(c.cli)

	nodePool, err := cli.GetNodePool(ctx, &nodepoolservice.GetNodePoolRequest{
		ClusterId:  clusterID,
		NodePoolId: nodePoolID,
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to delete node pool", err.Error())
		return
	}

	if nodePool.Status == "Running" || nodePool.Status == "Error" {
		_, err = cli.DeleteNodePool(ctx, &nodepoolservice.DeleteNodePoolRequest{
			ClusterId:  clusterID,
			NodePoolId: nodePoolID,
		})
		if err != nil {
			resp.Diagnostics.AddError("failed to delete node pool", err.Error())
			return
		}

	} else if nodePool.Status != "Deleting" {
		resp.Diagnostics.AddError("failed to delete node pool", "resource in the wrong state")
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {

		_, err = cli.GetNodePool(ctx, &nodepoolservice.GetNodePoolRequest{
			ClusterId:  clusterID,
			NodePoolId: nodePoolID,
		})
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return
		} else if err != nil {
			resp.Diagnostics.AddError("failed to delete node pool", err.Error())
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
func (c *nodePoolResource) Metadata(
	ctx context.Context,
	req resource.MetadataRequest,
	resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_node_pool_v1"
}

// Read implements resource.Resource.
func (c *nodePoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state nodePoolModel
	tflog.Debug(ctx, "read, getting state")
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsUnknown() {
		resp.Diagnostics.AddError("failed to read cluster state", "cluster id is not known")
		return
	}

	tflog.Debug(ctx, "read, refreshing node pool state")
	resp.Diagnostics.Append(refreshNodePoolState(ctx, c.cli, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "read, settings state")
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Schema implements resource.Resource.
func (c *nodePoolResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Description: "Id of the cluster.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(uuidRegex, "must be valid uuid"),
				},
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Id of the node pool.",
			},
			"name": schema.StringAttribute{
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description:   "Name of the node pool.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 64),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-_ ]*$`),
						"must start with character and contains only alphanumeric and -_ characters",
					),
				},
			},
			"flavor": schema.StringAttribute{
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description:   "Machine flavor.",
			},
			"autoscale": schema.BoolAttribute{
				Description: "Should node pool autoscale based on the usage? If set size_min and size_max must also be provided.",
				Computed:    true,
				Optional:    true,
				Default:     booldefault.StaticBool(false),
			},
			"size": schema.Int32Attribute{
				Description: "Size of the static node pool.",
				Optional:    true,
				Validators: []validator.Int32{
					int32validator.ConflictsWith(
						path.MatchRelative().AtParent().AtName("size_min"),
						path.MatchRelative().AtParent().AtName("size_max"),
					),
				},
			},
			"size_min": schema.Int32Attribute{
				Description: "Minimum size of the node pool when autoscale is turn on.",
				Optional:    true,
				Validators: []validator.Int32{
					int32validator.AlsoRequires(
						path.MatchRelative().AtParent().AtName("size_max"),
					),
				},
			},
			"size_max": schema.Int32Attribute{
				Description: "Maximum size of the node pool when autoscale is turn on.",
				Optional:    true,
				Validators: []validator.Int32{
					int32validator.AlsoRequires(
						path.MatchRelative().AtParent().AtName("size_min"),
					),
				},
			},
			"shared_networks": schema.ListAttribute{
				Description:   "A list of network ids that should be attached to the nodes in the node pool.",
				ElementType:   types.StringType,
				Optional:      true,
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"labels": schema.ListNestedAttribute{
				Description: "List of labels. Must followe standard kubernetes requirements.",
				Optional:    true,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
				Validators: []validator.List{
					listvalidator.SizeAtMost(50),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Required: true,
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
						"value": schema.StringAttribute{
							Optional: true,
							Validators: []validator.String{
								stringvalidator.LengthAtMost(63),
							},
						},
					},
				},
			},
			"taints": schema.ListNestedAttribute{
				Description:   "List of initial taints applied to the nodes of this node pool.",
				Optional:      true,
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				Validators:    []validator.List{listvalidator.SizeAtMost(50)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Required:   true,
							Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"value": schema.StringAttribute{
							Optional:   true,
							Validators: []validator.String{stringvalidator.LengthAtMost(63)},
						},
						"effect": schema.StringAttribute{
							Required: true,
							Validators: []validator.String{
								stringvalidator.OneOf("NoSchedule", "NoExecute", "PreferNoSchedule"),
							},
						},
					},
				},
			},
		},
	}
}

// Update implements resource.Resource.
func (c *nodePoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var current nodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &current)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var request nodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &request)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := current.ClusterID.ValueString()
	nodePoolID := current.ID.ValueString()

	cli := nodepoolservice.NewNodePoolClient(c.cli)

	nodePool, err := cli.GetNodePool(ctx, &nodepoolservice.GetNodePoolRequest{
		ClusterId:  clusterID,
		NodePoolId: nodePoolID,
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to update node pool", err.Error())
		return
	}

	nodePool.Autoscale = request.Autoscale.ValueBool()
	nodePool.Size = request.Size.ValueInt32Pointer()
	nodePool.SizeMin = request.SizeMin.ValueInt32Pointer()
	nodePool.SizeMax = request.SizeMax.ValueInt32Pointer()

	tflog.Info(ctx, "updated node pool", map[string]any{"object": nodePool})
	_, err = cli.UpdateNodePool(ctx, &nodepoolservice.UpdateNodePoolRequest{
		ClusterId:  clusterID,
		NodePoolId: nodePoolID,
		NodePool:   nodePool,
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to update node pool", err.Error())
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {

		resp.Diagnostics.Append(refreshNodePoolState(ctx, c.cli, &current)...)
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
			resp.Diagnostics.AddError("failed to update node pool", ctx.Err().Error())
		case <-ticker.C:
			continue
		}
	}
}
