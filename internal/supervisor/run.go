package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/client"
	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/connect"
)

// Main is the entry point for supervisor mode. It reads Config from stdin.
func Main() error {
	os.Unsetenv(ModeEnv)
	var cfg Config
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&cfg); err != nil {
		return fmt.Errorf("read supervisor config: %w", err)
	}
	os.Stdin.Close()

	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC|log.Lmicroseconds)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	return run(ctx, cfg, logger)
}

func run(ctx context.Context, cfg Config, logger *log.Logger) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	if cfg.ParentPID > 0 {
		go watchParent(ctx, cfg.ParentPID, func() {
			logger.Printf("terraform process %d exited; shutting down", cfg.ParentPID)
			cancel(errors.New("terraform exited"))
		})
	}

	// Short private base dir: twingated puts its IPC socket under
	// RUNTIME_DIRECTORY and unix socket paths are limited to ~108 bytes.
	base, err := os.MkdirTemp("", "tgt-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(base)
	dirs := map[string]string{}
	for _, n := range []string{"state", "run", "conf"} {
		d := filepath.Join(base, n)
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
		dirs[n] = d
	}

	proxyPort, err := freePort()
	if err != nil {
		return fmt.Errorf("pick proxy port: %w", err)
	}
	proxyAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))

	// Bind the forwarder before reporting so the port is held from the start.
	var ln net.Listener
	if cfg.ListenAddr != "" {
		ln, err = net.Listen("tcp", cfg.ListenAddr)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
		}
		defer ln.Close()
	}

	twLogPath := filepath.Join(cfg.LogDir, "twingated.log")
	twLog, err := os.OpenFile(twLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer twLog.Close()

	cmd := exec.Command(cfg.BinPath, "--http-proxy", proxyAddr, "--tun", "off")
	cmd.Dir = dirs["state"]
	cmd.Stdout, cmd.Stderr = twLog, twLog
	cmd.Env = twingatedEnv(cfg, dirs)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start twingated: %w", err)
	}
	logger.Printf("started twingated pid=%d proxy=%s", cmd.Process.Pid, proxyAddr)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer stopProcess(cmd, exited, logger)

	go func() {
		select {
		case err := <-exited:
			exited <- err // leave it for stopProcess
			cancel(fmt.Errorf("twingated exited: %v", err))
		case <-ctx.Done():
		}
	}()

	start := time.Now()
	// twingated's proxy accepts CONNECTs before it has authenticated, so
	// readiness is the Online state first, then a working probe tunnel.
	readyCtx, readyCancel := context.WithTimeout(ctx, cfg.ReadyTimeout)
	err = waitOnline(readyCtx, twLogPath, proxyAddr)
	if err == nil {
		logger.Printf("twingated online after %s", time.Since(start).Round(time.Millisecond))
		if cfg.ProbeDest != "" {
			err = connect.WaitReady(readyCtx, proxyAddr, cfg.ProbeDest, time.Second)
		}
	}
	readyCancel()
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return fmt.Errorf("not ready: %w", cause)
		}
		return fmt.Errorf("not ready after %s: %w", cfg.ReadyTimeout, err)
	}
	logger.Printf("ready after %s", time.Since(start).Round(time.Millisecond))

	ready := Ready{ProxyAddr: proxyAddr}
	if ln != nil {
		ready.ListenAddr = ln.Addr().String()
	}
	if err := writeReady(cfg.ReadyFile, ready); err != nil {
		return err
	}

	if ln != nil {
		fwd := &connect.Forwarder{ProxyAddr: proxyAddr, Dest: cfg.ProbeDest, Logger: logger}
		go func() {
			if err := fwd.Serve(ctx, ln); err != nil {
				cancel(fmt.Errorf("forwarder: %w", err))
			}
		}()
	}

	<-ctx.Done()
	logger.Printf("shutting down: %v", context.Cause(ctx))
	return nil
}

// twingatedEnv builds a minimal environment. STATE_DIRECTORY,
// RUNTIME_DIRECTORY and CONFIGURATION_DIRECTORY are the systemd variables
// twingated uses in place of /var/lib/twingate, /run/twingate and
// /etc/twingate, which makes it run without root.
func twingatedEnv(cfg Config, dirs map[string]string) []string {
	env := []string{
		"TWINGATE_SERVICE_KEY=" + cfg.ServiceKey,
		"STATE_DIRECTORY=" + dirs["state"],
		"RUNTIME_DIRECTORY=" + dirs["run"],
		"CONFIGURATION_DIRECTORY=" + dirs["conf"],
		"XDG_RUNTIME_DIR=" + dirs["run"],
		"HOME=" + dirs["state"],
	}
	if cfg.LibDir != "" {
		env = append(env, "LD_LIBRARY_PATH="+client.LibraryPathEnv(cfg.LibDir))
	}
	for _, k := range []string{"PATH", "TZ", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// clientState matches twingated's state log line, e.g.
// "State: 'Online', client mode: 'Headless'".
var clientState = regexp.MustCompile(`State: '([A-Za-z]+)'`)

// waitOnline waits until twingated's most recent logged state is Online and
// its proxy port accepts connections.
func waitOnline(ctx context.Context, logPath, proxyAddr string) error {
	last := ""
	for {
		if last = lastState(logPath); last == "Online" {
			if c, err := net.DialTimeout("tcp", proxyAddr, time.Second); err == nil {
				c.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if last == "" {
				last = "none logged"
			}
			return fmt.Errorf("%w (twingated state: %s)", ctx.Err(), last)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func lastState(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	state := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if m := clientState.FindSubmatch(sc.Bytes()); m != nil {
			state = string(m[1])
		}
	}
	return state
}

func writeReady(path string, r Ready) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// stopProcess sends SIGTERM to twingated's process group, then SIGKILL after
// a grace period.
func stopProcess(cmd *exec.Cmd, exited chan error, logger *log.Logger) {
	pgid := cmd.Process.Pid
	select {
	case <-exited:
		return
	default:
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-exited:
		logger.Printf("twingated stopped")
	case <-time.After(twingatedStopGrace):
		logger.Printf("twingated did not stop after %s; killing", twingatedStopGrace)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-exited
	}
}

func watchParent(ctx context.Context, pid int, onExit func()) {
	t := time.NewTicker(parentPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !Alive(pid) {
				onExit()
				return
			}
		}
	}
}

// Alive reports whether pid exists (signal 0 probe).
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
