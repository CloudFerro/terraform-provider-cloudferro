package cloudferro

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithUpgradeState = (*clusterResource)(nil)

type clusterModelV0 struct {
	ID           types.String             `tfsdk:"id"`
	Name         types.String             `tfsdk:"name"`
	Version      types.String             `tfsdk:"version"`
	ControlPlane clusterModelControlPlane `tfsdk:"control_plane"`
	Kubeconfig   types.String             `tfsdk:"kubeconfig"`
	Metadata     types.Object             `tfsdk:"metadata"`
	RouterIP     types.String             `tfsdk:"router_ip"`
}

var clusterSchemaV0 = schema.Schema{
	Attributes: map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed: true,
		},
		"version": schema.StringAttribute{
			Required: true,
		},
		"name": schema.StringAttribute{
			Required: true,
		},
		"control_plane": schema.SingleNestedAttribute{
			Required: true,
			Attributes: map[string]schema.Attribute{
				"size": schema.Int32Attribute{
					Required: true,
				},
				"flavor": schema.StringAttribute{
					Required: true,
				},
			},
		},
		"kubeconfig": schema.StringAttribute{
			Computed:  true,
			Sensitive: true,
		},
		"metadata": schema.SingleNestedAttribute{
			Computed: true,
			Attributes: map[string]schema.Attribute{
				"openstack_project_id": schema.StringAttribute{
					Computed: true,
				},
			},
		},
		"router_ip": schema.StringAttribute{
			Computed: true,
		},
	},
}

// UpgradeState implements resource.ResourceWithUpgradeState.
func (c *clusterResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		// kubeconfig changed from a raw string into an object with the parsed
		// connection details.
		0: {
			PriorSchema: &clusterSchemaV0,
			StateUpgrader: func(
				ctx context.Context,
				req resource.UpgradeStateRequest,
				resp *resource.UpgradeStateResponse,
			) {
				var prior clusterModelV0
				resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
				if resp.Diagnostics.HasError() {
					return
				}

				upgraded := clusterModel{
					ID:           prior.ID,
					Name:         prior.Name,
					Version:      prior.Version,
					ControlPlane: prior.ControlPlane,
					Kubeconfig:   types.ObjectNull(kubeconfigType),
					Metadata:     prior.Metadata,
					RouterIP:     prior.RouterIP,
					AllowedCIDRs: types.ListNull(types.StringType),
				}

				if raw := prior.Kubeconfig.ValueString(); raw != "" {
					config, err := kubeconfigFromConfig(raw)
					if err != nil {
						resp.Diagnostics.AddError("failed to upgrade cluster state", err.Error())
						return
					}

					obj, diags := types.ObjectValueFrom(ctx, kubeconfigType, config)
					resp.Diagnostics.Append(diags...)
					if resp.Diagnostics.HasError() {
						return
					}

					upgraded.Kubeconfig = obj
				}

				resp.Diagnostics.Append(resp.State.Set(ctx, upgraded)...)
			},
		},
	}
}
