package provider

import (
	"context"
	"net"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ ephemeral.EphemeralResource              = &httpProxyEphemeral{}
	_ ephemeral.EphemeralResourceWithConfigure = &httpProxyEphemeral{}
	_ ephemeral.EphemeralResourceWithClose     = &httpProxyEphemeral{}
)

// NewHTTPProxyEphemeral returns the twingatetunnel_http_proxy ephemeral resource.
func NewHTTPProxyEphemeral() ephemeral.EphemeralResource { return &httpProxyEphemeral{} }

type httpProxyEphemeral struct {
	data *providerData
}

type httpProxyModel struct {
	ProbeDestination types.String `tfsdk:"probe_destination"`
	ReadyTimeout     types.String `tfsdk:"ready_timeout"`
	ProxyURL         types.String `tfsdk:"proxy_url"`
	ProxyHost        types.String `tfsdk:"proxy_host"`
	ProxyPort        types.Int64  `tfsdk:"proxy_port"`
}

func (r *httpProxyEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_http_proxy"
}

func (r *httpProxyEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Starts the Twingate Client in HTTP proxy mode and returns the proxy URL. " +
			"Use it for providers with an explicit proxy setting, such as `kubernetes { proxy_url = ... }`.",
		Attributes: map[string]schema.Attribute{
			"probe_destination": schema.StringAttribute{
				MarkdownDescription: "Optional `host:port` of a Twingate Resource. The proxy is ready once the client reports Online and, " +
					"when this is set, a probe tunnel to it stays open.",
				Optional: true,
			},
			"ready_timeout": schema.StringAttribute{
				MarkdownDescription: "How long to wait for the proxy to be ready, as a Go duration. Defaults to `90s`.",
				Optional:            true,
			},
			"proxy_url": schema.StringAttribute{
				MarkdownDescription: "Proxy URL, e.g. `http://127.0.0.1:41234`.",
				Computed:            true,
			},
			"proxy_host": schema.StringAttribute{
				MarkdownDescription: "Proxy listen address.",
				Computed:            true,
			},
			"proxy_port": schema.Int64Attribute{
				MarkdownDescription: "Proxy listen port.",
				Computed:            true,
			},
		},
	}
}

func (r *httpProxyEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if d := providerDataFrom(req, resp); d != nil {
		r.data = d
	}
}

func (r *httpProxyEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var m httpProxyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}

	probe := m.ProbeDestination.ValueString()
	if probe != "" {
		if err := validateHostPort(probe); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("probe_destination"), "Invalid probe_destination", "Expected host:port: "+err.Error())
			return
		}
	}
	timeout, err := parseReadyTimeout(m.ReadyTimeout.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("ready_timeout"), "Invalid ready_timeout", err.Error())
		return
	}

	h, diags := openTunnel(ctx, r.data, openParams{probeDest: probe, readyTimeout: timeout}, resp.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	host, port, err := net.SplitHostPort(h.Ready.ProxyAddr)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected proxy address", h.Ready.ProxyAddr)
		return
	}
	p, _ := strconv.ParseInt(port, 10, 64)
	m.ProxyURL = types.StringValue("http://" + h.Ready.ProxyAddr)
	m.ProxyHost = types.StringValue(host)
	m.ProxyPort = types.Int64Value(p)
	resp.Diagnostics.Append(resp.Result.Set(ctx, &m)...)
}

func (r *httpProxyEphemeral) Close(ctx context.Context, req ephemeral.CloseRequest, resp *ephemeral.CloseResponse) {
	resp.Diagnostics.Append(closeTunnel(ctx, req.Private)...)
}
