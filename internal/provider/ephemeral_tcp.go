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
	_ ephemeral.EphemeralResource              = &tcpEphemeral{}
	_ ephemeral.EphemeralResourceWithConfigure = &tcpEphemeral{}
	_ ephemeral.EphemeralResourceWithClose     = &tcpEphemeral{}
)

// NewTCPEphemeral returns the twingatetunnel_tcp ephemeral resource.
func NewTCPEphemeral() ephemeral.EphemeralResource { return &tcpEphemeral{} }

type tcpEphemeral struct {
	data *providerData
}

type tcpModel struct {
	Destination  types.String `tfsdk:"destination"`
	LocalHost    types.String `tfsdk:"local_host"`
	LocalPort    types.Int64  `tfsdk:"local_port"`
	ReadyTimeout types.String `tfsdk:"ready_timeout"`
	ProxyURL     types.String `tfsdk:"proxy_url"`
}

func (r *tcpEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tcp"
}

func (r *tcpEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Opens a local TCP listener whose connections are forwarded through Twingate to a private Resource. " +
			"Use it for providers that speak raw TCP (PostgreSQL, MySQL, SSH, ...). " +
			"The tunnel is opened in every phase that needs it (plan and apply), and is closed when Terraform no longer needs it " +
			"or when the Terraform process exits.",
		Attributes: map[string]schema.Attribute{
			"destination": schema.StringAttribute{
				MarkdownDescription: "`host:port` of the Twingate Resource, e.g. `db.internal:5432`. The host is resolved by Twingate.",
				Required:            true,
			},
			"local_host": schema.StringAttribute{
				MarkdownDescription: "Local address to listen on. Defaults to `127.0.0.1`.",
				Optional:            true,
				Computed:            true,
			},
			"local_port": schema.Int64Attribute{
				MarkdownDescription: "Local port to listen on. Defaults to a random free port.",
				Optional:            true,
				Computed:            true,
			},
			"ready_timeout": schema.StringAttribute{
				MarkdownDescription: "How long to wait for the tunnel to work, as a Go duration. Defaults to `90s`. " +
					"Ready means the client reports Online and a probe tunnel to `destination` stays open.",
				Optional: true,
			},
			"proxy_url": schema.StringAttribute{
				MarkdownDescription: "URL of the Twingate HTTP proxy backing this tunnel, e.g. `http://127.0.0.1:41234`.",
				Computed:            true,
			},
		},
	}
}

func (r *tcpEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if d := providerDataFrom(req, resp); d != nil {
		r.data = d
	}
}

func (r *tcpEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var m tcpModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}

	dest := m.Destination.ValueString()
	if err := validateHostPort(dest); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("destination"), "Invalid destination", "Expected host:port: "+err.Error())
		return
	}
	timeout, err := parseReadyTimeout(m.ReadyTimeout.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("ready_timeout"), "Invalid ready_timeout", err.Error())
		return
	}
	host := m.LocalHost.ValueString()
	if host == "" {
		host = "127.0.0.1"
	}
	port := m.LocalPort.ValueInt64()
	if port < 0 || port > 65535 {
		resp.Diagnostics.AddAttributeError(path.Root("local_port"), "Invalid local_port", "Must be 0-65535.")
		return
	}

	h, diags := openTunnel(ctx, r.data, openParams{
		probeDest:    dest,
		listenAddr:   net.JoinHostPort(host, strconv.FormatInt(port, 10)),
		readyTimeout: timeout,
	}, resp.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, actualPort, err := net.SplitHostPort(h.Ready.ListenAddr)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected listener address", h.Ready.ListenAddr)
		return
	}
	p, _ := strconv.ParseInt(actualPort, 10, 64)
	m.LocalHost = types.StringValue(host)
	m.LocalPort = types.Int64Value(p)
	m.ProxyURL = types.StringValue("http://" + h.Ready.ProxyAddr)
	resp.Diagnostics.Append(resp.Result.Set(ctx, &m)...)
}

func (r *tcpEphemeral) Close(ctx context.Context, req ephemeral.CloseRequest, resp *ephemeral.CloseResponse) {
	resp.Diagnostics.Append(closeTunnel(ctx, req.Private)...)
}
