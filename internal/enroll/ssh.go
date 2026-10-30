package enroll

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type Remote struct {
	client    *ssh.Client
	agentConn net.Conn
}

func Dial(ctx context.Context, c Config) (*Remote, error) {
	var auth []ssh.AuthMethod
	var agentConn net.Conn
	if c.PasswordFile != "" {
		b, e := os.ReadFile(c.PasswordFile)
		if e != nil {
			return nil, e
		}
		auth = append(auth, ssh.Password(strings.TrimSpace(string(b))))
	}
	if c.PrivateKeyFile != "" {
		b, e := os.ReadFile(c.PrivateKeyFile)
		if e != nil {
			return nil, e
		}
		key, e := ssh.ParsePrivateKey(b)
		if e != nil {
			return nil, fmt.Errorf("invalid SSH private key")
		}
		auth = append(auth, ssh.PublicKeys(key))
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" && len(auth) == 0 {
		if conn, e := net.Dial("unix", sock); e == nil {
			agentConn = conn
			auth = append(auth, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("SSH passwordFile, privateKeyFile or agent required")
	}
	// Prefer the Ed25519 host identity pinned by the enrollment preflight.
	// The fingerprint callback remains mandatory; negotiation never trusts a new key.
	algorithms := []string{ssh.KeyAlgoED25519}
	for _, algorithm := range ssh.SupportedAlgorithms().HostKeys {
		if algorithm != ssh.KeyAlgoED25519 {
			algorithms = append(algorithms, algorithm)
		}
	}
	cfg := &ssh.ClientConfig{User: c.User, Auth: auth, HostKeyAlgorithms: algorithms, Timeout: 15 * time.Second, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != c.HostKeySHA256 {
			return fmt.Errorf("SSH host key mismatch")
		}
		return nil
	}}
	address := net.JoinHostPort(c.Host, fmt.Sprint(c.SSHPort))
	conn, e := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if e != nil {
		return nil, e
	}
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	cc, chans, reqs, e := ssh.NewClientConn(conn, address, cfg)
	if e != nil {
		conn.Close()
		if agentConn != nil {
			agentConn.Close()
		}
		return nil, fmt.Errorf("SSH handshake: %w", e)
	}
	conn.SetDeadline(time.Time{})
	return &Remote{client: ssh.NewClient(cc, chans, reqs), agentConn: agentConn}, nil
}
func (r *Remote) Close() {
	r.client.Close()
	if r.agentConn != nil {
		r.agentConn.Close()
	}
}
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (r *Remote) Run(ctx context.Context, script string, input []byte) ([]byte, error) {
	session, e := r.client.NewSession()
	if e != nil {
		return nil, e
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(input)
	var out, stderr bytes.Buffer
	session.Stdout = &out
	session.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- session.Run("bash -e -c " + Quote(script)) }()
	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGTERM)
		return nil, ctx.Err()
	case e = <-done:
	}
	// Never include script, input, or stderr: they may contain bootstrap credentials.
	if e != nil {
		return nil, fmt.Errorf("remote operation failed: %w", e)
	}
	return out.Bytes(), nil
}
func (r *Remote) Put(ctx context.Context, path string, data []byte, mode string) error {
	if mode != "0600" && mode != "0700" {
		return fmt.Errorf("unsupported remote file mode")
	}
	// Rename a complete file atomically, including when updating a running agent.
	_, e := r.Run(ctx, "umask 077; tmp=$(mktemp "+Quote(path+".XXXXXX")+"); trap 'rm -f -- \"$tmp\"' EXIT; cat > \"$tmp\"; chmod "+mode+" \"$tmp\"; mv -f -- \"$tmp\" "+Quote(path), data)
	return e
}
func ReadAllLimited(r io.Reader) ([]byte, error) { return io.ReadAll(io.LimitReader(r, 1<<20)) }
