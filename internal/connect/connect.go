// Package connect bridges raw TCP connections through an HTTP CONNECT proxy.
package connect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// DefaultDialTimeout bounds the proxy dial plus the CONNECT handshake.
const DefaultDialTimeout = 15 * time.Second

// Dial opens a tunnel to dest ("host:port") through the HTTP CONNECT proxy at
// proxyAddr. The returned connection is ready for raw bytes.
func Dial(ctx context.Context, proxyAddr, dest string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxyAddr, err)
	}
	// Bound the handshake; cleared once the tunnel is established.
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", dest, dest); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, &StatusError{Code: resp.StatusCode, Status: resp.Status}
	}
	if !stop() {
		conn.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})

	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// StatusError is a non-200 CONNECT response.
type StatusError struct {
	Code   int
	Status string
}

func (e *StatusError) Error() string { return "proxy refused CONNECT: " + e.Status }

// bufferedConn returns bytes the proxy sent after its response headers before
// reading from the socket again.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *bufferedConn) CloseWrite() error {
	if hc, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	return c.Close()
}

// ProbeHold is how long a probe tunnel must survive without EOF.
const ProbeHold = 1500 * time.Millisecond

// Probe checks that dest is reachable through the proxy. twingated answers
// "200 Connection established" before it has reached the destination, and
// reports failure by closing the tunnel, so a probe passes only if the tunnel
// then delivers data or stays open for hold.
func Probe(ctx context.Context, proxyAddr, dest string, hold time.Duration) error {
	conn, err := Dial(ctx, proxyAddr, dest, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(hold))
	var b [1]byte
	_, err = conn.Read(b[:])
	var ne net.Error
	if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		return nil
	}
	return fmt.Errorf("tunnel to %s closed: %w", dest, err)
}

// WaitReady polls Probe until it succeeds or ctx ends.
func WaitReady(ctx context.Context, proxyAddr, dest string, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	var lastErr error
	for {
		err := Probe(ctx, proxyAddr, dest, ProbeHold)
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last error: %v)", ctx.Err(), lastErr)
		case <-time.After(interval):
		}
	}
}

// Forwarder accepts TCP connections and relays each through the proxy to Dest.
type Forwarder struct {
	ProxyAddr string
	Dest      string
	Logger    *log.Logger
}

// Serve accepts on l until ctx is cancelled or l fails. Open relays are closed
// when ctx is cancelled.
func (f *Forwarder) Serve(ctx context.Context, l net.Listener) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns = map[net.Conn]struct{}{}
	)
	track := func(c net.Conn, add bool) {
		mu.Lock()
		defer mu.Unlock()
		if add {
			conns[c] = struct{}{}
		} else {
			delete(conns, c)
		}
	}
	stop := context.AfterFunc(ctx, func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for c := range conns {
			c.Close()
		}
	})
	defer stop()

	for {
		c, err := l.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		track(c, true)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer track(c, false)
			f.handle(ctx, c, track)
		}()
	}
}

func (f *Forwarder) handle(ctx context.Context, c net.Conn, track func(net.Conn, bool)) {
	up, err := Dial(ctx, f.ProxyAddr, f.Dest, 0)
	if err != nil {
		f.logf("connection from %s: %v", c.RemoteAddr(), err)
		c.Close()
		return
	}
	track(up, true)
	defer track(up, false)
	Relay(c, up)
}

func (f *Forwarder) logf(format string, args ...any) {
	if f.Logger != nil {
		f.Logger.Printf(format, args...)
	}
}

// Relay copies in both directions until both sides finish, propagating
// half-close so request/response protocols see EOF correctly. There is no
// idle timeout: long-lived database sessions are legitimate.
func Relay(a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		pipe(b, a)
		close(done)
	}()
	pipe(a, b)
	<-done
	a.Close()
	b.Close()
}

func pipe(dst, src net.Conn) {
	_, err := io.Copy(dst, src)
	if err != nil {
		// Unblock the opposite direction too.
		dst.Close()
		src.Close()
		return
	}
	if hc, ok := dst.(interface{ CloseWrite() error }); !ok || hc.CloseWrite() != nil {
		dst.Close()
	}
}
