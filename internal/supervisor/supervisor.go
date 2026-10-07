// Package supervisor runs twingated (and optionally a local TCP forwarder) in
// a process detached from the provider plugin. The provider re-executes its
// own binary in supervisor mode; the supervisor outlives plugin restarts and
// exits when Terraform core exits or when it receives SIGINT/SIGTERM.
package supervisor

import (
	"time"
)

// ModeEnv switches the provider binary into supervisor mode. The config is
// read from stdin so the service key never appears in the supervisor's
// environment or argv.
const ModeEnv = "TWINGATETUNNEL_SUPERVISOR"

// Config is sent from the provider to the supervisor over stdin as JSON.
type Config struct {
	// ParentPID is Terraform core. The supervisor exits when it disappears.
	ParentPID int `json:"parent_pid"`
	// ReadyFile receives a JSON Ready document once the tunnel works.
	ReadyFile string `json:"ready_file"`
	// LogDir receives supervisor.log and twingated.log for this instance.
	LogDir string `json:"log_dir"`

	BinPath    string `json:"bin_path"`
	LibDir     string `json:"lib_dir"`
	ServiceKey string `json:"service_key"`

	// ProbeDest is CONNECTed through the proxy to decide readiness.
	ProbeDest    string        `json:"probe_dest"`
	ReadyTimeout time.Duration `json:"ready_timeout"`

	// ListenAddr enables the TCP forwarder ("host:port", port may be 0).
	// Forwarded connections go to ProbeDest.
	ListenAddr string `json:"listen_addr,omitempty"`
}

// Ready is written to Config.ReadyFile when the tunnel is usable.
type Ready struct {
	ProxyAddr  string `json:"proxy_addr"`
	ListenAddr string `json:"listen_addr,omitempty"`
}

const (
	twingatedStopGrace = 5 * time.Second
	parentPollInterval = 2 * time.Second
)
