package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"nhooyr.io/websocket"

	"github.com/codewiresh/codewire/internal/peer"
	"github.com/codewiresh/codewire/internal/platform"
	"github.com/codewiresh/codewire/internal/terminal"
	"github.com/codewiresh/tailnet"
)

func sshCmd() *cobra.Command {
	var stdio bool

	cmd := &cobra.Command{
		Use:   "ssh [env-id-or-name]",
		Short: "Open a shell in a running environment",
		Long: `Connect to a running sandbox environment via SSH.

Use this for an environment shell.
Use 'cw attach' to re-open the terminal of a specific Codewire run.
Use 'cw run' to start a run inside the selected environment.

Interactive mode (default):
  Connects via SSH with PTY, resize support, and Ctrl+B d to detach.

Stdio mode (--stdio):
  For use as SSH ProxyCommand. Pipes stdin/stdout directly to the SSH proxy.
  Used by: ssh cw-<envid> (via ~/.ssh/config ProxyCommand)

For VS Code Remote-SSH, run 'cw config-ssh' to configure ~/.ssh/config.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: envCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := ""
			if len(args) > 0 {
				ref = args[0]
				// Strip "cw-" prefix for ProxyCommand use (Host cw-*)
				if strings.HasPrefix(ref, "cw-") {
					ref = ref[3:]
				}
			}

			target, err := requireEnvironmentTarget(ref)
			if err != nil {
				return err
			}
			orgID, client, err := getOrgContext(cmd)
			if err != nil {
				return err
			}
			envID := target.Ref

			if stdio {
				return sshStdio(client, orgID, envID)
			}
			return sshInteractive(client, orgID, envID)
		},
	}

	cmd.Flags().BoolVar(&stdio, "stdio", false, "Stdio mode for ProxyCommand (pipe stdin/stdout to SSH proxy)")
	cmd.Flags().String("org", "", "Organization ID or slug (default: current org)")
	return cmd
}

// sshStdio connects via WireGuard (primary) or WebSocket proxy (fallback)
// and pipes stdin/stdout. Used as ProxyCommand for VS Code and ssh clients.
func sshStdio(client *platform.Client, orgID, envID string) error {
	// Try WireGuard first.
	err := sshStdioWireGuard(client, orgID, envID)
	if err == nil {
		return nil
	}

	// Fall back to WebSocket proxy.
	return sshStdioWebSocket(client, orgID, envID)
}

// sshStdioWebSocket connects to the SSH proxy WebSocket and pipes stdin/stdout.
func sshStdioWebSocket(client *platform.Client, orgID, envID string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsURL := strings.Replace(client.ServerURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += fmt.Sprintf("/api/v1/organizations/%s/environments/%s/ssh-proxy", orgID, envID)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"ssh"},
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + client.SessionToken},
		},
	})
	if err != nil {
		return fmt.Errorf("ssh proxy connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Set up signal handler
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
	}()

	done := make(chan error, 2)

	// stdin -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if wErr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); wErr != nil {
					done <- wErr
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()

	// WebSocket -> stdout
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			if _, err := os.Stdout.Write(data); err != nil {
				done <- err
				return
			}
		}
	}()

	err = <-done
	if err == io.EOF {
		return nil
	}
	return err
}

// sshInteractive connects to the environment via SSH over WireGuard (primary)
// or WebSocket proxy (fallback).
func sshInteractive(client *platform.Client, orgID, envID string) error {
	// Try WireGuard first.
	err := sshOverWireGuard(client, orgID, envID)
	if err == nil {
		return nil
	}

	// Classify the WireGuard failure for diagnostics.
	errMsg := err.Error()
	switch {
	case strings.Contains(errMsg, "timeout waiting for agent peer info"):
		fmt.Fprintf(os.Stderr, "wireguard: no peer info from sidecar (coordinator exchange timed out)\n")
		fmt.Fprintf(os.Stderr, "  hint: sidecar may be on a different server replica, or DERP relay unreachable\n")
		fmt.Fprintf(os.Stderr, "  debug: CW_DEBUG_TAILNET=1 cw ssh %s\n", envID)
	case strings.Contains(errMsg, "coordinator connect"):
		fmt.Fprintf(os.Stderr, "wireguard: coordinator WebSocket failed (%v)\n", err)
	case strings.Contains(errMsg, "dial agent ssh"):
		fmt.Fprintf(os.Stderr, "wireguard: peer found but SSH dial failed (%v)\n", err)
		fmt.Fprintf(os.Stderr, "  hint: DERP relay may be unreachable from sidecar, or sidecar SSH not listening\n")
	case strings.Contains(errMsg, "ssh handshake"):
		fmt.Fprintf(os.Stderr, "wireguard: tunnel OK but SSH handshake failed (%v)\n", err)
	default:
		fmt.Fprintf(os.Stderr, "wireguard unavailable (%v)\n", err)
	}
	fmt.Fprintln(os.Stderr, "trying websocket proxy...")

	// Check if SSH proxy is available
	available, _ := client.CheckSSHProxy(orgID, envID)
	if !available {
		fmt.Fprintln(os.Stderr, "websocket proxy: sshd not reachable via port-forward")
		fmt.Fprintln(os.Stderr, "  hint: sidecar may not be running, or exec-check endpoint failing")
		return terminalFallback(client, orgID, envID)
	}

	return sshOverWebSocket(client, orgID, envID)
}

// connectWireGuard creates a WireGuard tunnel to the environment's agent and
// returns a TCP connection to the agent's SSH port (22).
func connectWireGuard(ctx context.Context, client *platform.Client, orgID, envID string) (net.Conn, *tailnet.Conn, error) {
	tcpConn, wgConn, err := peer.DialEnvironmentPeerTCP(ctx, client, orgID, envID, 22)
	if err != nil {
		return nil, nil, err
	}
	return tcpConn, wgConn, nil
}

// sshOverWireGuard establishes an SSH connection through WireGuard.
func sshOverWireGuard(client *platform.Client, orgID, envID string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tcpConn, wgConn, err := connectWireGuard(ctx, client, orgID, envID)
	if err != nil {
		return err
	}
	defer wgConn.Close()

	sshConfig := &ssh.ClientConfig{
		User: "codewire",
		Auth: []ssh.AuthMethod{ssh.Password("")},
	}
	sshConfig.HostKeyCallback, err = codewireHostKeyCallback()
	if err != nil {
		return fmt.Errorf("known_hosts callback: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, "cw-"+envID+":22", sshConfig)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}
	defer sshConn.Close()

	return runSSHSession(ssh.NewClient(sshConn, chans, reqs))
}

// sshStdioWireGuard connects via WireGuard and pipes stdin/stdout to the
// raw TCP connection. The calling SSH client handles SSH protocol.
func sshStdioWireGuard(client *platform.Client, orgID, envID string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tcpConn, wgConn, err := connectWireGuard(ctx, client, orgID, envID)
	if err != nil {
		return err
	}
	defer wgConn.Close()
	defer tcpConn.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
		tcpConn.Close()
	}()

	done := make(chan error, 2)

	// stdin -> TCP
	go func() {
		_, err := io.Copy(tcpConn, os.Stdin)
		done <- err
	}()

	// TCP -> stdout
	go func() {
		_, err := io.Copy(os.Stdout, tcpConn)
		done <- err
	}()

	err = <-done
	if err == io.EOF {
		return nil
	}
	return err
}

// sshOverWebSocket establishes an SSH connection through the WebSocket proxy.
// Uses "none" auth — the workspace SSH server runs with NoClientAuth since
// network-layer authentication (WireGuard or server-side auth) handles identity.
func sshOverWebSocket(client *platform.Client, orgID, envID string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsURL := strings.Replace(client.ServerURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += fmt.Sprintf("/api/v1/organizations/%s/environments/%s/ssh-proxy", orgID, envID)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"ssh"},
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + client.SessionToken},
		},
	})
	if err != nil {
		return fmt.Errorf("ssh proxy connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	wsConn := &wsNetConn{conn: conn, ctx: ctx}

	sshConfig := &ssh.ClientConfig{
		User: "codewire",
		Auth: []ssh.AuthMethod{ssh.Password("")},
	}
	sshConfig.HostKeyCallback, err = codewireHostKeyCallback()
	if err != nil {
		return fmt.Errorf("known_hosts callback: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(wsConn, "cw-"+envID+":22", sshConfig)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}
	defer sshConn.Close()

	return runSSHSession(ssh.NewClient(sshConn, chans, reqs))
}

// runSSHSession opens a PTY session on the given SSH client with detach support.
func runSSHSession(sshClient *ssh.Client) error {
	defer sshClient.Close()

	session, err := sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	cols, rows, err := terminal.TerminalSize()
	if err != nil {
		cols, rows = 80, 24
	}

	if err := session.RequestPty("xterm-256color", int(rows), int(cols), ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return fmt.Errorf("request pty: %w", err)
	}

	rawGuard, err := terminal.EnableRawMode()
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer rawGuard.Restore()

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Shell(); err != nil {
		return fmt.Errorf("start shell: %w", err)
	}

	resizeCh, resizeCleanup := terminal.ResizeSignal()
	defer resizeCleanup()

	detach := terminal.NewDetachDetector()
	done := make(chan error, 1)

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				detached, fwd := detach.FeedBuf(buf[:n])
				if detached {
					rawGuard.Restore()
					fmt.Fprintln(os.Stderr, "\nDetached.")
					done <- nil
					return
				}
				if len(fwd) > 0 {
					if _, wErr := stdinPipe.Write(fwd); wErr != nil {
						done <- wErr
						return
					}
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()

	go func() {
		for range resizeCh {
			c, r, err := terminal.TerminalSize()
			if err == nil {
				session.WindowChange(int(r), int(c))
			}
		}
	}()

	sessionDone := make(chan error, 1)
	go func() {
		sessionDone <- session.Wait()
	}()

	select {
	case err := <-done:
		return err
	case err := <-sessionDone:
		rawGuard.Restore()
		if err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				os.Exit(exitErr.ExitStatus())
			}
		}
		return nil
	}
}

// terminalFallback uses the existing terminal WebSocket for environments without sshd.
func terminalFallback(client *platform.Client, orgID, envID string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsURL := strings.Replace(client.ServerURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += fmt.Sprintf("/api/v1/organizations/%s/environments/%s/terminal", orgID, envID)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"terminal"},
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + client.SessionToken},
		},
	})
	if err != nil {
		return fmt.Errorf("terminal connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Enable raw mode
	rawGuard, err := terminal.EnableRawMode()
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer rawGuard.Restore()

	// Send initial resize
	cols, rows, _ := terminal.TerminalSize()
	if cols > 0 && rows > 0 {
		resizeMsg := make([]byte, 5)
		resizeMsg[0] = 0x01 // msgTypeResize
		resizeMsg[1] = byte(cols >> 8)
		resizeMsg[2] = byte(cols)
		resizeMsg[3] = byte(rows >> 8)
		resizeMsg[4] = byte(rows)
		conn.Write(ctx, websocket.MessageBinary, resizeMsg)
	}

	// Handle SIGWINCH
	resizeCh, resizeCleanup := terminal.ResizeSignal()
	defer resizeCleanup()

	go func() {
		for range resizeCh {
			c, r, err := terminal.TerminalSize()
			if err == nil {
				msg := make([]byte, 5)
				msg[0] = 0x01
				msg[1] = byte(c >> 8)
				msg[2] = byte(c)
				msg[3] = byte(r >> 8)
				msg[4] = byte(r)
				conn.Write(ctx, websocket.MessageBinary, msg)
			}
		}
	}()

	done := make(chan error, 2)
	detach := terminal.NewDetachDetector()

	// stdin -> WebSocket (with terminal framing: 0x00 prefix for stdin)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				detached, fwd := detach.FeedBuf(buf[:n])
				if detached {
					rawGuard.Restore()
					fmt.Fprintln(os.Stderr, "\nDetached.")
					done <- nil
					return
				}
				if len(fwd) > 0 {
					// Prepend stdin message type
					msg := make([]byte, 1+len(fwd))
					msg[0] = 0x00 // msgTypeStdin
					copy(msg[1:], fwd)
					if wErr := conn.Write(ctx, websocket.MessageBinary, msg); wErr != nil {
						done <- wErr
						return
					}
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()

	// WebSocket -> stdout (raw bytes, no framing prefix on output)
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			os.Stdout.Write(data)
		}
	}()

	<-done
	return nil
}

// wsNetConn wraps a nhooyr.io/websocket.Conn to implement io.ReadWriteCloser
// for use with golang.org/x/crypto/ssh.NewClientConn.
type wsNetConn struct {
	conn   *websocket.Conn
	ctx    context.Context
	reader io.Reader
}

func (w *wsNetConn) Read(p []byte) (int, error) {
	for {
		if w.reader != nil {
			n, err := w.reader.Read(p)
			if n > 0 {
				return n, nil
			}
			if err != io.EOF {
				return 0, err
			}
			w.reader = nil
		}
		_, reader, err := w.conn.Reader(w.ctx)
		if err != nil {
			return 0, err
		}
		w.reader = reader
	}
}

func (w *wsNetConn) Write(p []byte) (int, error) {
	err := w.conn.Write(w.ctx, websocket.MessageBinary, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsNetConn) Close() error {
	return w.conn.Close(websocket.StatusNormalClosure, "")
}

func (w *wsNetConn) LocalAddr() net.Addr  { return wsAddr{} }
func (w *wsNetConn) RemoteAddr() net.Addr { return wsAddr{} }

func (w *wsNetConn) SetDeadline(t time.Time) error      { return nil }
func (w *wsNetConn) SetReadDeadline(t time.Time) error  { return nil }
func (w *wsNetConn) SetWriteDeadline(t time.Time) error { return nil }

type wsAddr struct{}

func (wsAddr) Network() string { return "websocket" }
func (wsAddr) String() string  { return "websocket:22" }
