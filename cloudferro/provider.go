package cloudferro

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
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
	Cli    *grpc.ClientConn
	Region string
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
	Region     types.String `tfsdk:"region"`
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

	if config.Host.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("host"),
			"Unknown CloudFerro API Host",
			"The provider cannot create the CloudFerro API client as there is an unknown configuration value "+
				"for the CloudFerro API host. Either target apply the source of the value first, set the value statically "+
				"in the configuration, or use the CLOUDFERRO_HOST environment variable",
		)
	}

	if config.Token.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Unknown CloudFerro API Token",
			"The provider cannot create the CloudFerro API client as there is an unknown configuration value "+
				"for the CloudFerro API token. Either target apply the source of the value first, set the value statically "+
				"in the configuration, or use the CLOUDFERRO_TOKEN environment variable",
		)
	}

	if config.Region.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("region"),
			"Unknown CloudFerro API Region",
			"The provider cannot create the CloudFerro API client as there is an unknown configuration value "+
				"for the CloudFerro API region. Either target apply the source of the value first, set the value statically "+
				"in the configuration, or use the CLOUDFERRO_REGION environment variable",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	host := os.Getenv("CLOUDFERRO_HOST")
	token := os.Getenv("CLOUDFERRO_TOKEN")
	region := os.Getenv("CLOUDFERRO_REGION")

	cert := os.Getenv("CLOUDFERRO_CERT")

	if !config.Host.IsNull() {
		host = config.Host.ValueString()
	}

	if !config.Token.IsNull() {
		token = config.Token.ValueString()
	}

	if !config.Region.IsNull() {
		region = config.Region.ValueString()
	}

	if !config.ServerCert.IsNull() {
		cert = config.ServerCert.ValueString()
	}

	if host == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("host"),
			"Missing CloudFerro API Host",
			"The provider cannot create the CloudFerro API client as there is a missing or empty value for the CloudFerro API host. "+
				"Set the host value in the configuration or use the CLOUDFERRO_HOST environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing CloudFerro API Token",
			"The provider cannot create the CloudFerro API client as there is a missing or empty value for the CloudFerro API token. "+
				"Set the host value in the configuration or use the CLOUDFERRO_TOKEN environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}
	if region == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("region"),
			"Missing CloudFerro API Region",
			"The provider cannot create the CloudFerro API client as there is a missing or empty value for the CloudFerro API region. "+
				"Set the host value in the configuration or use the CLOUDFERRO_REGION environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	var err error
	var creds credentials.TransportCredentials

	if cert != "" {
		creds, err = credentials.NewClientTLSFromFile(cert, "")
		if err != nil {
			resp.Diagnostics.AddError("failed to configure provider", fmt.Sprintf("failed to load certificates: %v", err))
			return
		}
	} else {
		var pool *x509.CertPool
		pool, err = x509.SystemCertPool()
		if err != nil {
			resp.Diagnostics.AddError("failed to configure provider", fmt.Sprintf("failed to load system certificates: %v", err))
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

	state := &providerState{Cli: cli, Region: region}

	resp.DataSourceData = state
	resp.ResourceData = state
}

// DataSources implements provider.Provider.
func (m *CloudFerroProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
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
			"host": schema.StringAttribute{
				Optional:    true,
				Description: "Address of the CloudFerro Managed Kubernetes service. Should be in the form of <host>:<port> or <host> if port is 443. Can be omitted if the `CLOUDFERRO_HOST` environment variable is set.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "API Token for the CloudFerro Managed Kubernetes service. Can be omitted if the `CLOUDFERRO_TOKEN` environment variable is set.",
			},
			"server_cert": schema.StringAttribute{
				Optional:    true,
				Description: "Path to a PEM-encoded certificate file for the CloudFerro Managed Kubernetes service. Can be omitted if the `CLOUDFERRO_CERT` environment variable is set.",
			},
			"region": schema.StringAttribute{
				Optional:    true,
				Description: "Region of the CloudFerro Managed Kubernetes service. Can be omitted if the `CLOUDFERRO_REGION` environment variable is set.",
			},
		},
	}
}
