package relay

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/codewiresh/codewire/internal/store"
)

func TestNewSSHServerPersistsHostKey(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.NodeRegister(ctx, store.NodeRecord{
		NetworkID:    "project-alpha",
		Name:         "dev-1",
		Token:        "node-token",
		AuthorizedAt: time.Now().UTC(),
		LastSeenAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("NodeRegister: %v", err)
	}

	hub := NewNodeHub()
	sessions := NewPendingSessions()
	if _, err := NewSSHServer(dataDir, st, hub, sessions); err != nil {
		t.Fatalf("NewSSHServer first: %v", err)
	}
	keyPath := filepath.Join(dataDir, "ssh_host_ed25519")
	firstPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile first host key: %v", err)
	}
	if _, err := NewSSHServer(dataDir, st, hub, sessions); err != nil {
		t.Fatalf("NewSSHServer second: %v", err)
	}
	secondPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile second host key: %v", err)
	}
	if string(firstPEM) != string(secondPEM) {
		t.Fatal("expected persisted SSH host key to remain stable across restarts")
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat host key: %v", err)
	}
	if perms := info.Mode().Perm(); perms != 0o600 {
		t.Fatalf("host key mode = %o, want 600", perms)
	}
}

func TestSSHServerUsesPersistedHostKeyForHandshake(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	if err := st.NodeRegister(context.Background(), store.NodeRecord{
		NetworkID:    "project-alpha",
		Name:         "dev-1",
		Token:        "node-token",
		AuthorizedAt: now,
		LastSeenAt:   now,
	}); err != nil {
		t.Fatalf("NodeRegister: %v", err)
	}

	sshSrv, err := NewSSHServer(dataDir, st, NewNodeHub(), NewPendingSessions())
	if err != nil {
		t.Fatalf("NewSSHServer: %v", err)
	}
	keyBytes, err := os.ReadFile(filepath.Join(dataDir, "ssh_host_ed25519"))
	if err != nil {
		t.Fatalf("ReadFile host key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("tcp listen unavailable in this test environment: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sshSrv.Serve(ctx, ln)

	clientConfig := &ssh.ClientConfig{
		User:            "project-alpha/dev-1",
		Auth:            []ssh.AuthMethod{ssh.Password("node-token")},
		HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
		Timeout:         5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", ln.Addr().String(), clientConfig)
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}
	_ = conn.Close()
}
