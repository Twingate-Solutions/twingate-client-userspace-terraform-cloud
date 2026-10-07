// Package provider implements the twingatetunnel Terraform provider.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/client"
)

const serviceKeyEnv = "TWINGATE_SERVICE_KEY"

var (
	_ provider.Provider                       = &twingateTunnelProvider{}
	_ provider.ProviderWithEphemeralResources = &twingateTunnelProvider{}
)

// New returns a provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &twingateTunnelProvider{version: version}
	}
}

type twingateTunnelProvider struct {
	version string
}

type providerModel struct {
	ServiceKey            types.String `tfsdk:"service_key"`
	ClientVersion         types.String `tfsdk:"client_version"`
	ClientPath            types.String `tfsdk:"client_path"`
	ClientURL             types.String `tfsdk:"client_url"`
	ClientSHA256          types.String `tfsdk:"client_sha256"`
	FetchMissingLibraries types.Bool   `tfsdk:"fetch_missing_libraries"`
	LogDir                types.String `tfsdk:"log_dir"`
	CacheDir              types.String `tfsdk:"cache_dir"`
}

func (p *twingateTunnelProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "twingatetunnel"
	resp.Version = p.version
}

func (p *twingateTunnelProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Runs the Twingate Client (`twingated`) in unprivileged userspace HTTP-proxy mode for the " +
			"duration of a Terraform run, so other providers can reach private Twingate Resources. " +
			"Works on HCP Terraform hosted workers: no root, TUN device, custom image or agent is needed. Linux only.",
		Attributes: map[string]schema.Attribute{
			"service_key": schema.StringAttribute{
				MarkdownDescription: "Twingate Service Account key (the full JSON document). Defaults to the `TWINGATE_SERVICE_KEY` environment variable. " +
					"It is passed to `twingated` through its environment only and never written to disk.",
				Optional:  true,
				Sensitive: true,
			},
			"client_version": schema.StringAttribute{
				MarkdownDescription: "Version of the `twingate` package to download from the Twingate apt repository, e.g. `2026.239.6882`. " +
					"Defaults to the newest version available. Pin this for repeatable runs.",
				Optional: true,
			},
			"client_path": schema.StringAttribute{
				MarkdownDescription: "Path to a preinstalled `twingated` binary. When set, nothing is downloaded. Useful for agents and local development.",
				Optional:            true,
			},
			"client_url": schema.StringAttribute{
				MarkdownDescription: "URL of a `twingate` `.deb` to download instead of reading the Twingate apt index, e.g. from an internal artifact store. Requires `client_sha256`.",
				Optional:            true,
			},
			"client_sha256": schema.StringAttribute{
				MarkdownDescription: "Hex SHA-256 of the `.deb` at `client_url`.",
				Optional:            true,
			},
			"fetch_missing_libraries": schema.BoolAttribute{
				MarkdownDescription: "Download shared libraries that `twingated` needs but the host lacks (for example `libcryptsetup.so.12`, `libnl-3.so.200`) " +
					"from the frozen Ubuntu 20.04 release repository, verified by SHA-256. Defaults to `true`.",
				Optional: true,
			},
			"log_dir": schema.StringAttribute{
				MarkdownDescription: "Directory for supervisor and `twingated` logs. Defaults to `<tmp>/twingatetunnel/logs`.",
				Optional:            true,
			},
			"cache_dir": schema.StringAttribute{
				MarkdownDescription: "Directory for the downloaded client and libraries. Defaults to `<tmp>/twingatetunnel/cache`.",
				Optional:            true,
			},
		},
	}
}

// providerData is shared with the ephemeral resources.
type providerData struct {
	serviceKey string
	clientOpts client.Options
	logDir     string

	mu   sync.Mutex
	inst *client.Installation
}

// installation obtains twingated once per provider process.
func (d *providerData) installation(ctx context.Context) (*client.Installation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inst != nil {
		return d.inst, nil
	}
	opts := d.clientOpts
	opts.Logf = func(format string, args ...any) {
		tflog.Info(ctx, fmt.Sprintf(format, args...))
	}
	inst, err := client.Ensure(ctx, opts)
	if err != nil {
		return nil, err
	}
	d.inst = inst
	return inst, nil
}

func (p *twingateTunnelProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Values derived from ephemeral or not-yet-known inputs are unknown during
	// some phases. The ephemeral resources are not opened then, so defer.
	if cfg.ServiceKey.IsUnknown() || cfg.ClientVersion.IsUnknown() || cfg.ClientPath.IsUnknown() ||
		cfg.ClientURL.IsUnknown() || cfg.ClientSHA256.IsUnknown() || cfg.LogDir.IsUnknown() || cfg.CacheDir.IsUnknown() {
		return
	}

	key := cfg.ServiceKey.ValueString()
	if key == "" {
		key = os.Getenv(serviceKeyEnv)
	}
	if key != "" && !json.Valid([]byte(key)) {
		resp.Diagnostics.AddAttributeError(path.Root("service_key"), "Invalid Twingate service key",
			"The service key must be the full JSON document downloaded from the Twingate Admin Console. (Value not shown.)")
		return
	}

	base := filepath.Join(os.TempDir(), "twingatetunnel")
	logDir := cfg.LogDir.ValueString()
	if logDir == "" {
		logDir = filepath.Join(base, "logs")
	}
	cacheDir := cfg.CacheDir.ValueString()
	if cacheDir == "" {
		cacheDir = filepath.Join(base, "cache")
	}
	if (cfg.ClientURL.ValueString() == "") != (cfg.ClientSHA256.ValueString() == "") {
		resp.Diagnostics.AddError("Incomplete client override", "client_url and client_sha256 must be set together.")
		return
	}

	fetchLibs := true
	if !cfg.FetchMissingLibraries.IsNull() && !cfg.FetchMissingLibraries.IsUnknown() {
		fetchLibs = cfg.FetchMissingLibraries.ValueBool()
	}

	data := &providerData{
		serviceKey: key,
		logDir:     logDir,
		clientOpts: client.Options{
			Path:           cfg.ClientPath.ValueString(),
			Version:        cfg.ClientVersion.ValueString(),
			DebURL:         cfg.ClientURL.ValueString(),
			DebSHA256:      cfg.ClientSHA256.ValueString(),
			CacheDir:       cacheDir,
			FetchLibraries: fetchLibs,
		},
	}
	resp.EphemeralResourceData = data
}

func (p *twingateTunnelProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewTCPEphemeral,
		NewHTTPProxyEphemeral,
	}
}

func (p *twingateTunnelProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *twingateTunnelProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
