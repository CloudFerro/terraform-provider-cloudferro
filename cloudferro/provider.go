package cloudferro

import (
	"context"
	"crypto/x509"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var _ credentials.PerRPCCredentials = (*tokenAuth)(nil)

type tokenAuth struct {
	token string
}

// GetRequestMetadata implements [credentials.PerRPCCredentials].
func (t tokenAuth) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{
		"Authorization": "Token " + t.token,
	}, nil
}

// RequireTransportSecurity implements [credentials.PerRPCCredentials].
func (t tokenAuth) RequireTransportSecurity() bool {
	return true
}

type providerState struct {
	Cli *grpc.ClientConn
}

var _ provider.Provider = (*CloudFerroProvider)(nil)

func NewProvider(version string) func() provider.Provider {
	return func() provider.Provider {
		return &CloudFerroProvider{
			version: version,
		}
	}
}

type cloudFerroConfigModel struct {
	Host       types.String `tfsdk:"host"`
	ServerCert types.String `tfsdk:"server_cert"`
	Token      types.String `tfsdk:"token"`
}

type CloudFerroProvider struct {
	version string
}

// Configure implements provider.Provider.
func (m *CloudFerroProvider) Configure(
	c context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse,
) {
	var config cloudFerroConfigModel
	resp.Diagnostics.Append(req.Config.Get(c, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// check if value are known and stuff

	host := config.Host.ValueString()
	token := config.Token.ValueString()
	cert := config.ServerCert.ValueString()

	var err error
	var creds credentials.TransportCredentials

	if cert != "" {
		creds, err = credentials.NewClientTLSFromFile(cert, "")
		if err != nil {
			resp.Diagnostics.AddError("failed to load certificates", fmt.Sprintf("%v", err))
			return
		}
	} else {
		var pool *x509.CertPool
		pool, err = x509.SystemCertPool()
		if err != nil {
			resp.Diagnostics.AddError("failed to load certificates", fmt.Sprintf("%v", err))
			return
		}
		creds = credentials.NewClientTLSFromCert(pool, "")
	}

	cli, err := grpc.NewClient(
		host,
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(
			grpc.PerRPCCredentials(tokenAuth{token: token}),
		),
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"failed to create client",
			fmt.Sprintf("Could not create a grpc backend client due to some error: %v", err),
		)
	}

	state := &providerState{Cli: cli}

	resp.DataSourceData = state
	resp.ResourceData = state
}

// DataSources implements provider.Provider.
func (m *CloudFerroProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newMachineSpecDataSource,
		newKubernetesVersionDataSource,
	}
}

// Metadata implements provider.Provider.
func (m *CloudFerroProvider) Metadata(
	ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse,
) {
	resp.TypeName = "cloudferro"
	resp.Version = m.version
}

// Resources implements provider.Provider.
func (m *CloudFerroProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newClusterResource,
		newNodePoolResource,
	}
}

// Schema implements provider.Provider.
func (m *CloudFerroProvider) Schema(_ context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"host":        schema.StringAttribute{Required: true},
			"token":       schema.StringAttribute{Required: true, Sensitive: true},
			"server_cert": schema.StringAttribute{Optional: true},
		},
	}
}
