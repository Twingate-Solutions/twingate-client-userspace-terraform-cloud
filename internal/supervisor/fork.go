package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Handle describes a running supervisor.
type Handle struct {
	PID    int
	LogDir string
	Ready  Ready
}

// Start re-executes the current binary in supervisor mode and waits until the
// supervisor reports readiness, exits, or ctx/the timeout expires. cfg.LogDir
// must exist; ReadyFile and ParentPID are filled in here.
func Start(ctx context.Context, cfg Config, timeout time.Duration) (*Handle, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate provider binary: %w", err)
	}

	cfg.ParentPID = os.Getppid()
	cfg.ReadyFile = filepath.Join(cfg.LogDir, "ready.json")
	_ = os.Remove(cfg.ReadyFile)

	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	logFile, err := os.OpenFile(filepath.Join(cfg.LogDir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), ModeEnv+"=1")
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// New session: terminal signals aimed at Terraform's process group must
	// not tear the tunnel down mid-operation; Close and the parent watch do.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start supervisor: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	fail := func(err error) (*Handle, error) {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
		return nil, err
	}

	for {
		if b, err := os.ReadFile(cfg.ReadyFile); err == nil {
			var r Ready
			if err := json.Unmarshal(b, &r); err != nil {
				return fail(fmt.Errorf("parse ready file: %w", err))
			}
			return &Handle{PID: cmd.Process.Pid, LogDir: cfg.LogDir, Ready: r}, nil
		}
		select {
		case err := <-exited:
			if err == nil {
				err = errors.New("exited without becoming ready")
			}
			return nil, fmt.Errorf("supervisor failed: %w", err)
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-deadline.C:
			return fail(fmt.Errorf("supervisor not ready after %s", timeout))
		case <-tick.C:
		}
	}
}

// Stop sends SIGINT to the supervisor and waits up to timeout for it to exit.
// It returns nil if the process is already gone.
func Stop(pid int, timeout time.Duration) error {
	// Guard against PID reuse: only signal a process running this binary.
	if self, err := os.Executable(); err == nil {
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err != nil || exe != self {
			return nil
		}
	}
	if err := syscall.Kill(pid, syscall.SIGINT); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if !Alive(pid) || isZombie(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("supervisor %d still running after %s", pid, timeout)
}

// isZombie reports whether pid has exited but not been reaped (possible when
// this provider process is its parent and has not called Wait yet).
func isZombie(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
}
