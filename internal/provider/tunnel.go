package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/supervisor"
)

const (
	defaultReadyTimeout = 90 * time.Second
	privatePIDKey       = "supervisor_pid"
	privateLogDirKey    = "log_dir"
	closeTimeout        = 15 * time.Second
	logTailLines        = 40
)

type openParams struct {
	probeDest    string
	listenAddr   string
	readyTimeout time.Duration
}

func providerDataFrom(req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) *providerData {
	if req.ProviderData == nil {
		return nil
	}
	d, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return nil
	}
	return d
}

func parseReadyTimeout(s string) (time.Duration, error) {
	if s == "" {
		return defaultReadyTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return d, nil
}

func validateHostPort(s string) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}
	return nil
}

// openTunnel ensures twingated is available, starts a supervisor and records
// its PID in private data for Close.
func openTunnel(ctx context.Context, data *providerData, p openParams, private interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}) (*supervisor.Handle, diag.Diagnostics) {
	var diags diag.Diagnostics
	if data == nil {
		diags.AddError("Provider not configured", "The twingatetunnel provider configuration was not available.")
		return nil, diags
	}
	if data.serviceKey == "" {
		diags.AddError("Missing Twingate service key",
			"Set service_key in the provider block or the TWINGATE_SERVICE_KEY environment variable (mark it sensitive in HCP Terraform).")
		return nil, diags
	}

	start := time.Now()
	inst, err := data.installation(ctx)
	if err != nil {
		diags.AddError("Failed to obtain the Twingate client", err.Error())
		return nil, diags
	}
	tflog.Info(ctx, "twingated available", map[string]any{
		"version": inst.Version, "path": inst.BinPath, "elapsed": time.Since(start).String(),
	})

	if err := os.MkdirAll(data.logDir, 0o700); err != nil {
		diags.AddError("Failed to create log directory", err.Error())
		return nil, diags
	}
	logDir, err := os.MkdirTemp(data.logDir, "tunnel-")
	if err != nil {
		diags.AddError("Failed to create log directory", err.Error())
		return nil, diags
	}

	cfg := supervisor.Config{
		LogDir:       logDir,
		BinPath:      inst.BinPath,
		LibDir:       inst.LibDir,
		ServiceKey:   data.serviceKey,
		ProbeDest:    p.probeDest,
		ReadyTimeout: p.readyTimeout,
		ListenAddr:   p.listenAddr,
	}
	start = time.Now()
	h, err := supervisor.Start(ctx, cfg, p.readyTimeout+15*time.Second)
	if err != nil {
		diags.AddError("Twingate tunnel failed to start", failureDetail(err, logDir, data.serviceKey))
		return nil, diags
	}
	tflog.Info(ctx, "twingate tunnel ready", map[string]any{
		"supervisor_pid": h.PID, "proxy": h.Ready.ProxyAddr, "listen": h.Ready.ListenAddr,
		"elapsed": time.Since(start).String(), "log_dir": logDir,
	})

	// Private state values must be JSON.
	logDirJSON, _ := json.Marshal(logDir)
	diags.Append(private.SetKey(ctx, privatePIDKey, []byte(strconv.Itoa(h.PID)))...)
	diags.Append(private.SetKey(ctx, privateLogDirKey, logDirJSON)...)
	return h, diags
}

func closeTunnel(ctx context.Context, private interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}) diag.Diagnostics {
	var diags diag.Diagnostics
	raw, d := private.GetKey(ctx, privatePIDKey)
	diags.Append(d...)
	if diags.HasError() || len(raw) == 0 {
		return diags
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		diags.AddError("Invalid tunnel state", "could not parse supervisor PID")
		return diags
	}
	if err := supervisor.Stop(pid, closeTimeout); err != nil {
		// The supervisor also exits by itself when Terraform exits.
		diags.AddWarning("Twingate tunnel did not stop cleanly", err.Error())
	}
	return diags
}

// failureDetail builds a diagnostic body from the error and the redacted tail
// of the instance logs.
func failureDetail(err error, logDir, serviceKey string) string {
	secrets := []string{serviceKey}
	var k map[string]any
	if json.Unmarshal([]byte(serviceKey), &k) == nil {
		for _, f := range []string{"private_key", "key_id"} {
			if s, ok := k[f].(string); ok {
				secrets = append(secrets, s)
			}
		}
	}
	var b strings.Builder
	b.WriteString(supervisor.Redact(err.Error(), secrets...))
	for _, name := range []string{"supervisor.log", "twingated.log"} {
		p := filepath.Join(logDir, name)
		if t := supervisor.Tail(p, logTailLines); t != "" {
			fmt.Fprintf(&b, "\n\nLast lines of %s:\n%s", p, supervisor.Redact(t, secrets...))
		}
	}
	return b.String()
}
