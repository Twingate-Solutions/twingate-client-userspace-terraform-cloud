// Package testutil provides a fake twingated and network helpers for tests.
package testutil

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FakeTwingatedArg marks a test binary invocation as the fake twingated.
const FakeTwingatedArg = "fake-twingated"

// ServeConnectProxy serves HTTP CONNECT on l. Destinations for which allow
// returns false get 403. It returns when l is closed.
func ServeConnectProxy(l net.Listener, allow func(dest string) bool) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go handleConnect(c, allow)
	}
}

func handleConnect(c net.Conn, allow func(string) bool) {
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		c.Close()
		return
	}
	dest := req.Host
	if allow != nil && !allow(dest) {
		fmt.Fprint(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		c.Close()
		return
	}
	up, err := net.DialTimeout("tcp", dest, 5*time.Second)
	if err != nil {
		fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		c.Close()
		return
	}
	fmt.Fprint(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	go func() {
		io.Copy(up, br)
		if tc, ok := up.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()
	io.Copy(c, up)
	c.Close()
	up.Close()
}

// EchoServer starts a TCP echo server and returns its address.
func EchoServer(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(c, c)
				c.Close()
			}()
		}
	}()
	return l.Addr().String()
}

// WriteFakeTwingated writes a script that re-executes the current test binary
// as a fake twingated, and returns its path. The calling package's TestMain
// must call MaybeRunFakeTwingated before m.Run.
func WriteFakeTwingated(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "twingated")
	script := fmt.Sprintf("#!/bin/sh\nexec %q %s \"$@\"\n", os.Args[0], FakeTwingatedArg)
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// MaybeRunFakeTwingated runs the fake twingated and exits if the process was
// started by the script from WriteFakeTwingated. Otherwise it returns.
//
// The fake checks that it received the environment twingated needs, prints
// the Online state, and serves an unrestricted CONNECT proxy on --http-proxy.
func MaybeRunFakeTwingated() {
	args := os.Args
	i := 0
	for ; i < len(args) && args[i] != FakeTwingatedArg; i++ {
	}
	if i == len(args) {
		return
	}
	args = args[i+1:]

	var proxy string
	for j := 0; j < len(args); j++ {
		switch args[j] {
		case "--version":
			fmt.Println("0.0.0-fake")
			os.Exit(0)
		case "--http-proxy":
			if j+1 < len(args) {
				proxy = args[j+1]
			}
		}
	}
	for _, k := range []string{"TWINGATE_SERVICE_KEY", "STATE_DIRECTORY", "RUNTIME_DIRECTORY", "CONFIGURATION_DIRECTORY"} {
		if os.Getenv(k) == "" {
			fmt.Printf("fake twingated: %s not set\n", k)
			os.Exit(3)
		}
	}
	l, err := net.Listen("tcp", proxy)
	if err != nil {
		fmt.Println("fake twingated:", err)
		os.Exit(4)
	}
	fmt.Println("[INFO] [client] State: 'Online', client mode: 'Headless'")
	ServeConnectProxy(l, nil)
	os.Exit(0)
}
