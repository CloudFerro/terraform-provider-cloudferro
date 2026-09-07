package cloudferro

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ planmodifier.Object = kubeconfigModifier{}

// kubeconfigModifier is UseStateForUnknown, except for an expired kubeconfig,
// where it keeps host and plans only the credentials as unknown. Issuing new
// credentials does not move the cluster endpoint, so providers configured from
// host keep a known value while the credentials are renewed.
type kubeconfigModifier struct{}

func (m kubeconfigModifier) Description(_ context.Context) string {
	return "Once set, the kubeconfig in state is reused until its client certificate expires."
}

func (m kubeconfigModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m kubeconfigModifier) PlanModifyObject(
	_ context.Context,
	req planmodifier.ObjectRequest,
	resp *planmodifier.ObjectResponse,
) {
	if req.StateValue.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}

	if !kubeconfigExpired(req.StateValue) {
		resp.PlanValue = req.StateValue
		return
	}

	host, ok := req.StateValue.Attributes()["host"].(types.String)
	if !ok || host.IsNull() || host.IsUnknown() {
		return
	}

	resp.PlanValue = types.ObjectValueMust(kubeconfigType, map[string]attr.Value{
		"host":                   host,
		"raw":                    types.StringUnknown(),
		"client_certificate":     types.StringUnknown(),
		"client_key":             types.StringUnknown(),
		"cluster_ca_certificate": types.StringUnknown(),
	})
}
