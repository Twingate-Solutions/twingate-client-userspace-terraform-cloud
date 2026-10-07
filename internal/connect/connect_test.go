package connect

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/testutil"
)

func startProxy(t *testing.T, allow func(string) bool) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go testutil.ServeConnectProxy(l, allow)
	return l.Addr().String()
}

func TestDialRoundTrip(t *testing.T) {
	echo := testutil.EchoServer(t)
	proxy := startProxy(t, nil)

	c, err := Dial(context.Background(), proxy, echo, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("got %q, %v", buf, err)
	}
}

func TestDialRefused(t *testing.T) {
	proxy := startProxy(t, func(string) bool { return false })
	_, err := Dial(context.Background(), proxy, "db.internal:5432", time.Second)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("want 403 StatusError, got %v", err)
	}
}

func TestDialTimeoutOnSilentProxy(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close() // never answers
		}
	}()
	start := time.Now()
	if _, err := Dial(context.Background(), l.Addr().String(), "x:1", 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("dial timeout not enforced")
	}
}

// twingated answers 200 first and closes the tunnel if the destination is
// unreachable; Probe must treat that as failure.
func TestProbeFailsOnEarlyClose(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 512)
			c.Read(buf)
			c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
			c.Close()
		}
	}()
	if err := Probe(context.Background(), l.Addr().String(), "db.internal:5432", time.Second); err == nil {
		t.Fatal("expected probe failure on immediate close")
	}

	echo := testutil.EchoServer(t)
	if err := Probe(context.Background(), startProxy(t, nil), echo, 200*time.Millisecond); err != nil {
		t.Fatalf("open silent tunnel should pass: %v", err)
	}
}

func TestWaitReadyBecomesReady(t *testing.T) {
	echo := testutil.EchoServer(t)
	ready := make(chan struct{})
	proxy := startProxy(t, func(string) bool {
		select {
		case <-ready:
			return true
		default:
			return false
		}
	})
	time.AfterFunc(300*time.Millisecond, func() { close(ready) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitReady(ctx, proxy, echo, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	proxy := startProxy(t, func(string) bool { return false })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := WaitReady(ctx, proxy, "db.internal:5432", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want timeout mentioning last 403, got %v", err)
	}
}

func TestForwarderHalfClose(t *testing.T) {
	// Server reads until EOF, then replies: only works if half-close propagates.
	srv, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		c, err := srv.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c)
		c.Write([]byte("got:" + string(b)))
		c.Close()
	}()

	proxy := startProxy(t, nil)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&Forwarder{ProxyAddr: proxy, Dest: srv.Addr().String()}).Serve(ctx, l) }()

	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("hello"))
	c.(*net.TCPConn).CloseWrite()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := io.ReadAll(c)
	if err != nil || string(b) != "got:hello" {
		t.Fatalf("got %q, %v", b, err)
	}
	c.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}
