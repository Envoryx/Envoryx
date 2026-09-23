package sshd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/project"
)

// Remote forwarding (ssh -R) makes a port on the container's localhost lead back to the
// client – PyCharm's SSH interpreter needs it to run anything. The listener has to live in
// the container's network namespace, so socat listens there and connects every accepted
// connection to Envoryx on the project network; Envoryx accepts only the container's
// address and hands each connection to the client as a forwarded-tcpip channel.

// remoteForwardScript runs socat until stdin closes: Envoryx closes it when the client
// cancels the forward or the SSH connection ends, since cancelling an exec does not stop
// the process in the container.
const remoteForwardScript = `socat TCP4-LISTEN:"$1",bind=127.0.0.1,reuseaddr,fork TCP4:"$2" & pid=$!
cat >/dev/null
kill "$pid" 2>/dev/null
wait "$pid" 2>/dev/null
exit 0`

type forwardRequest struct {
	BindAddr string
	BindPort uint32
}

type forwardedTCPPayload struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

// remoteForwards tracks one SSH connection's active forwards by "addr:port" as requested.
type remoteForwards struct {
	mu     sync.Mutex
	active map[string]func()
}

func (f *remoteForwards) add(key string, stop func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active == nil {
		f.active = map[string]func(){}
	}
	f.active[key] = stop
}

func (f *remoteForwards) cancel(key string) bool {
	f.mu.Lock()
	stop, ok := f.active[key]
	delete(f.active, key)
	f.mu.Unlock()
	if ok {
		stop()
	}
	return ok
}

func (f *remoteForwards) stopAll() {
	f.mu.Lock()
	all := f.active
	f.active = nil
	f.mu.Unlock()
	for _, stop := range all {
		stop()
	}
}

// handleGlobalRequests serves remote forwarding for one connection and answers every
// other global request (keepalives …) with a failure, as before.
func (s *Server) handleGlobalRequests(ctx context.Context, sc ssh.Conn, reqs <-chan *ssh.Request, target project.ExecTarget) {
	var forwards remoteForwards
	defer forwards.stopAll()
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			var fr forwardRequest
			if err := ssh.Unmarshal(req.Payload, &fr); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			port, stop, err := s.startRemoteForward(ctx, sc, target, fr)
			if err != nil {
				s.d.Log.Warn("ssh remote forward rejected", "project", target.Project.Slug, "bind", net.JoinHostPort(fr.BindAddr, strconv.Itoa(int(fr.BindPort))), "err", err)
				_ = req.Reply(false, nil)
				continue
			}
			forwards.add(net.JoinHostPort(fr.BindAddr, strconv.Itoa(int(fr.BindPort))), stop)
			var reply []byte
			if fr.BindPort == 0 {
				reply = ssh.Marshal(struct{ Port uint32 }{port})
			}
			_ = req.Reply(true, reply)
		case "cancel-tcpip-forward":
			var fr forwardRequest
			ok := ssh.Unmarshal(req.Payload, &fr) == nil && forwards.cancel(net.JoinHostPort(fr.BindAddr, strconv.Itoa(int(fr.BindPort))))
			_ = req.Reply(ok, nil)
		default:
			if req.Type != "keepalive@openssh.com" {
				s.d.Log.Info("ssh global request not supported", "project", target.Project.Slug, "type", req.Type)
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// startRemoteForward sets up one forward and returns the container port it listens on and
// a function that tears it down.
func (s *Server) startRemoteForward(ctx context.Context, sc ssh.Conn, target project.ExecTarget, fr forwardRequest) (uint32, func(), error) {
	switch fr.BindAddr {
	case "", "localhost", "127.0.0.1":
	default:
		return 0, nil, fmt.Errorf("only localhost of the project container can listen, not %q", fr.BindAddr)
	}
	if !target.Running {
		return 0, nil, errors.New("project is not running")
	}
	if !s.hasSocat(ctx, target) {
		return 0, nil, errors.New("the runtime image has no socat – update it")
	}
	envoryxIP, containerIP, err := s.d.Projects.CallbackAddresses(ctx, target)
	if err != nil {
		return 0, nil, err
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort(envoryxIP, "0"))
	if err != nil {
		return 0, nil, fmt.Errorf("listen on the project network: %w", err)
	}
	fctx, cancel := context.WithCancel(ctx)
	stop := func() { cancel(); _ = ln.Close() }

	port, err := s.startContainerListener(fctx, target, fr.BindPort, ln.Addr().String())
	if err != nil {
		stop()
		return 0, nil, err
	}
	s.d.Log.Info("ssh remote forward", "project", target.Project.Slug, "port", port)
	s.d.Audit.Log(ctx, "ssh.remote_forward", "project", target.Project.ID, map[string]any{"name": target.Project.Name, "port": port})

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			remote, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
			if remote != containerIP {
				s.d.Log.Warn("ssh remote forward: connection from a foreign address refused", "project", target.Project.Slug, "from", remote)
				_ = conn.Close()
				continue
			}
			go s.forwardToClient(sc, conn, fr.BindAddr, port)
		}
	}()
	return port, stop, nil
}

// startContainerListener runs the socat listener in the container and waits until it
// accepts connections. A requested port of 0 means any free one.
func (s *Server) startContainerListener(ctx context.Context, target project.ExecTarget, want uint32, envoryxAddr string) (uint32, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		port := want
		if port == 0 {
			port = uint32(32768 + rand.IntN(28000))
		}
		stdinR, stdinW := io.Pipe()
		exited := make(chan struct{})
		go func() {
			defer close(exited)
			_, _ = s.d.Engine.ExecStream(context.WithoutCancel(ctx), target.ContainerID, docker.ExecStreamOptions{
				Cmd:   []string{"sh", "-c", remoteForwardScript, "envoryx-forward", strconv.Itoa(int(port)), envoryxAddr},
				User:  target.User,
				Stdin: stdinR,
			})
		}()
		go func() {
			select {
			case <-ctx.Done():
			case <-exited:
			}
			_ = stdinW.Close()
		}()
		if s.waitListening(ctx, target, int(port), exited) {
			return port, nil
		}
		_ = stdinW.Close()
		<-exited
		lastErr = fmt.Errorf("could not listen on port %d inside %s", port, target.ContainerName)
		if want != 0 {
			break
		}
	}
	return 0, lastErr
}

// waitListening polls the container's socket table until port is listening, the listener
// process ended or a few seconds passed.
func (s *Server) waitListening(ctx context.Context, target project.ExecTarget, port int, exited <-chan struct{}) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.listenAddress(ctx, target, port); err == nil {
			return true
		}
		select {
		case <-exited:
			return false
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

// forwardToClient opens a forwarded-tcpip channel for one connection from the container.
func (s *Server) forwardToClient(sc ssh.Conn, conn net.Conn, bindAddr string, port uint32) {
	defer conn.Close()
	// The origin is the process in the container; its real address is on the container's
	// loopback, the port is socat's (clients refuse port 0).
	_, originPort, _ := net.SplitHostPort(conn.RemoteAddr().String())
	op, _ := strconv.ParseUint(originPort, 10, 16)
	ch, reqs, err := sc.OpenChannel("forwarded-tcpip", ssh.Marshal(forwardedTCPPayload{
		Addr: bindAddr, Port: port, OriginAddr: "127.0.0.1", OriginPort: uint32(op),
	}))
	if err != nil {
		s.d.Log.Warn("ssh remote forward: client refused the channel", "port", port, "err", err)
		return
	}
	go ssh.DiscardRequests(reqs)
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(ch, conn)
		_ = ch.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, ch)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = ch.Close()
}
