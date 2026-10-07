package supervisor

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.MaybeRunFakeTwingated()
	if os.Getenv(ModeEnv) != "" {
		if err := Main(); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeTwingatedPIDs lists running fake twingated processes from this test binary.
func fakeTwingatedPIDs(t *testing.T) []string {
	t.Helper()
	ents, _ := os.ReadDir("/proc")
	var out []string
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(testutil.FakeTwingatedArg)) && bytes.Contains(b, []byte(os.Args[0])) && !isZombieName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func isZombieName(pid string) bool {
	b, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		return true
	}
	i := bytes.LastIndexByte(b, ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
}

func TestStartForwardStop(t *testing.T) {
	echo := testutil.EchoServer(t)
	logDir := t.TempDir()
	h, err := Start(context.Background(), Config{
		LogDir:       logDir,
		BinPath:      testutil.WriteFakeTwingated(t),
		ServiceKey:   `{"private_key":"secret"}`,
		ProbeDest:    echo,
		ReadyTimeout: 10 * time.Second,
		ListenAddr:   "127.0.0.1:0",
	}, 20*time.Second)
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, Tail(filepath.Join(logDir, "supervisor.log"), 20), Tail(filepath.Join(logDir, "twingated.log"), 20))
	}

	c, err := net.DialTimeout("tcp", h.Ready.ListenAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("got %q, %v", buf, err)
	}
	c.Close()

	if len(fakeTwingatedPIDs(t)) == 0 {
		t.Fatal("expected a running fake twingated")
	}
	if err := Stop(h.PID, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(fakeTwingatedPIDs(t)) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if p := fakeTwingatedPIDs(t); len(p) > 0 {
		t.Fatalf("twingated still running: %v", p)
	}
	if _, err := net.DialTimeout("tcp", h.Ready.ListenAddr, time.Second); err == nil {
		t.Fatal("listener still accepting after Stop")
	}
}

func TestStartFailsWhenNotReady(t *testing.T) {
	logDir := t.TempDir()
	_, err := Start(context.Background(), Config{
		LogDir:       logDir,
		BinPath:      testutil.WriteFakeTwingated(t),
		ServiceKey:   "{}",
		ProbeDest:    "127.0.0.1:1", // nothing listens; proxy answers 502
		ReadyTimeout: time.Second,
	}, 10*time.Second)
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(Tail(filepath.Join(logDir, "supervisor.log"), 20), "not ready") {
		t.Errorf("supervisor.log lacks reason:\n%s", Tail(filepath.Join(logDir, "supervisor.log"), 20))
	}
}

func TestRunExitsWhenParentExits(t *testing.T) {
	parent := exec.Command("sleep", "2")
	if err := parent.Start(); err != nil {
		t.Skip("sleep unavailable:", err)
	}
	go parent.Wait()

	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), Config{
			ParentPID:    parent.Process.Pid,
			ReadyFile:    filepath.Join(t.TempDir(), "ready.json"),
			LogDir:       t.TempDir(),
			BinPath:      testutil.WriteFakeTwingated(t),
			ServiceKey:   "{}",
			ReadyTimeout: 10 * time.Second,
		}, log.New(io.Discard, "", 0))
	}()
	select {
	case err := <-done:
		// Exiting before readiness is also acceptable; the point is exiting.
		if err != nil && !strings.Contains(err.Error(), "terraform exited") {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not exit after parent exited")
	}
}

func TestRedact(t *testing.T) {
	key := `{"version":"1","private_key":"-----BEGIN PRIVATE KEY-----\nMIIabc\n-----END PRIVATE KEY-----","key_id":"kid-123456"}`
	in := "loaded " + key + " token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijkl Authorization: Bearer zzz"
	out := Redact(in, key)
	for _, leak := range []string{"MIIabc", "kid-123456", "eyJhbGciOiJIUzI1NiJ9", "zzz"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in %q", leak, out)
		}
	}
	out = Redact(`"private_key": "abc", "token":"t0k"`)
	if strings.Contains(out, "abc") || strings.Contains(out, "t0k") {
		t.Errorf("field redaction failed: %q", out)
	}
}
