package cloudferro

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	hostPrefix = "managed-kubernetes"
	hostSuffix = "cloudferro.com"
)

func gethost(region string) string {
	return fmt.Sprintf("%s.%s.%s", hostPrefix, region, hostSuffix)
}

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

	if token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing CloudFerro API Token",
			"The provider cannot create the CloudFerro API client as there is a missing or empty value for the CloudFerro API token. "+
				"Set the host value in the configuration or use the CLOUDFERRO_TOKEN environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}
	if host == "" {
		if region == "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("region"),
				"Missing CloudFerro API Region",
				"The provider cannot create the CloudFerro API client as there is a missing or empty value for the CloudFerro API region. "+
					"Set the region value in the configuration or use the CLOUDFERRO_REGION environment variable. "+
					"If either is already set, ensure the value is not empty.",
			)
		} else {
			host = gethost(region)
		}
	} else {
		if region != "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("region"),
				"Region and Host are mutually exclusive",
				"The provider cannot create the CloudFerro API client as both region and host are set. "+
					"Either set the region value in the configuration or use the CLOUDFERRO_REGION environment variable, "+
					"or set the host value in the configuration or use the CLOUDFERRO_HOST environment variable. ",
			)
		}
	}

	if resp.Diagnostics.HasError() {
		return
	}

	var err error

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
		resp.Diagnostics.AddWarning("failed to configure provider", fmt.Sprintf("failed to load system certificates: %v", err))
	}

	if cert != "" {
		var certData []byte
		certData, err = os.ReadFile(cert)
		if err != nil {
			resp.Diagnostics.AddError("failed to configure provider", fmt.Sprintf("failed to load certificate: %v", err))
			return
		}

		if !pool.AppendCertsFromPEM(certData) {
			resp.Diagnostics.AddError("failed to configure provider", "credentials: failed to append certificates")
			return
		}
	}

	cli, err := grpc.NewClient(
		host,
		grpc.WithTransportCredentials(
			credentials.NewClientTLSFromCert(pool, ""),
		),
		grpc.WithAuthority(formatAuthority(host)),
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
				Optional: true,
				Description: "Address of the CloudFerro Managed Kubernetes service. Should be in the form of `host:port` " +
					"or `host` if port is 443. Can be omitted if the `CLOUDFERRO_HOST` environment variable is set. " +
					"Should be only really used for private endpoints.",
				Validators: []validator.String{
					stringvalidator.ConflictsWith(
						path.MatchRelative().AtParent().AtName("region"),
					),
				},
			},
			"token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "API Token for the CloudFerro Managed Kubernetes service. Can be omitted if " +
					"the `CLOUDFERRO_TOKEN` environment variable is set.",
			},
			"server_cert": schema.StringAttribute{
				Optional: true,
				Description: "Path to a PEM-encoded certificate file for the CloudFerro Managed Kubernetes service. " +
					"Can be omitted if the `CLOUDFERRO_CERT` environment variable is set.",
			},
			"region": schema.StringAttribute{
				Optional: true,
				Description: "Region of the CloudFerro Managed Kubernetes service. Can be omitted if " +
					"the `CLOUDFERRO_REGION` environment variable is set.",
			},
		},
	}
}

func formatAuthority(host string) string {
	parts := strings.Split(host, ":")
	if len(parts) == 1 {
		return host
	}

	if parts[1] == "443" || parts[1] == "80" {
		return parts[0]
	}

	return host
}
