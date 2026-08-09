package tunnel

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ssh-socks5/internal/config"
	"ssh-socks5/internal/logger"
	"golang.org/x/crypto/ssh"
)

type Manager struct {
	cfg          *config.Config
	log          *logger.Logger
	mu           sync.Mutex
	client       *ssh.Client
	udpRelay     *UDPRelay
	sessions     atomic.Int64
	idleTimer    *time.Timer
	idleCancel   context.CancelFunc
	connecting   bool
	connectCond  *sync.Cond
	closed       bool
	shutdown     chan struct{}
	tunnelStartedAt time.Time
	bytesTransferred atomic.Uint64
}

func NewManager(cfg *config.Config, log *logger.Logger) *Manager {
	m := &Manager{
		cfg:      cfg,
		log:      log,
		shutdown: make(chan struct{}),
	}
	m.connectCond = sync.NewCond(&m.mu)
	return m
}

func (m *Manager) Client() *ssh.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client
}

func (m *Manager) Acquire(ctx context.Context) error {
	m.sessions.Add(1)

	m.mu.Lock()
	m.stopIdleTimerLocked()
	m.mu.Unlock()

	if err := m.ensureConnected(ctx); err != nil {
		m.Release()
		return err
	}
	return nil
}

func (m *Manager) Release() {
	remaining := m.sessions.Add(-1)
	if remaining < 0 {
		m.sessions.Store(0)
		return
	}
	if remaining == 0 {
		m.startIdleTimer()
	}
}

func (m *Manager) Shutdown(ctx context.Context) error {
	close(m.shutdown)

	m.mu.Lock()
	m.closed = true
	m.stopIdleTimerLocked()
	client := m.client
	relay := m.udpRelay
	m.client = nil
	m.udpRelay = nil
	m.mu.Unlock()

	if relay != nil {
		relay.Close()
	}
	if client != nil {
		m.logTunnelClose("closing SSH tunnel")
		_ = client.Close()
	}
	return nil
}

func (m *Manager) ensureConnected(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for {
		if m.closed {
			return fmt.Errorf("tunnel manager is shut down")
		}
		if m.client != nil {
			return nil
		}
		if !m.connecting {
			return m.connectLocked(ctx)
		}

		waitCtx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-waitCtx.Done():
				m.connectCond.Broadcast()
			case <-m.shutdown:
				m.connectCond.Broadcast()
			}
		}()

		m.connectCond.Wait()
		cancel()

		if m.closed {
			return fmt.Errorf("tunnel manager is shut down")
		}
		if m.client != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (m *Manager) connectLocked(ctx context.Context) error {
	m.connecting = true
	defer func() {
		m.connecting = false
		m.connectCond.Broadcast()
	}()

	m.log.Infof("establishing SSH tunnel to %s", m.cfg.SSH.Address())
	if m.cfg.SSH.HostKey.Verify {
		m.log.Debugf("SSH host key verification is enabled")
	} else {
		m.log.Debugf("SSH host key verification is disabled (InsecureIgnoreHostKey)")
	}

	client, err := dialSSH(ctx, m.cfg)
	if err != nil {
		m.log.Errorf("SSH connection to %s failed: %v", m.cfg.SSH.Address(), err)
		return err
	}

	var relay *UDPRelay
	if m.cfg.UDP.Enabled {
		relay, err = startUDPRelay(ctx, client, m.cfg, m.log, m.addBytes)
		if err != nil {
			_ = client.Close()
			m.log.Errorf("UDP relay setup failed: %v", err)
			return err
		}
	}

	m.client = client
	m.udpRelay = relay
	m.tunnelStartedAt = time.Now()
	m.bytesTransferred.Store(0)
	if relay != nil {
		m.log.Infof("SSH tunnel established (TCP + UDP)")
	} else {
		m.log.Infof("SSH tunnel established (TCP only)")
	}

	go m.watchConnection(client)

	return nil
}

func (m *Manager) watchConnection(client *ssh.Client) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.shutdown:
			return
		case <-ticker.C:
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			if err != nil {
				m.handleDisconnect(client)
				return
			}
		}
	}
}

func (m *Manager) handleDisconnect(client *ssh.Client) {
	m.mu.Lock()
	if m.client != client {
		m.mu.Unlock()
		return
	}
	m.log.Errorf("SSH connection lost")
	m.logTunnelCloseLocked("SSH connection lost, closing tunnel")
	m.client = nil
	if m.udpRelay != nil {
		m.udpRelay.Close()
		m.udpRelay = nil
	}
	active := m.sessions.Load()
	m.mu.Unlock()

	if active > 0 {
		m.log.Debugf("reconnecting SSH tunnel (%d active sessions)", active)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := m.ensureConnected(ctx); err != nil {
			m.log.Errorf("SSH reconnection failed: %v", err)
		}
	}
}

func (m *Manager) startIdleTimer() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopIdleTimerLocked()

	timeout := m.cfg.IdleTimeout.Duration
	m.log.Debugf("no active sessions, scheduling tunnel shutdown in %s", timeout)

	ctx, cancel := context.WithCancel(context.Background())
	m.idleCancel = cancel
	m.idleTimer = time.AfterFunc(timeout, func() {
		select {
		case <-ctx.Done():
			return
		case <-m.shutdown:
			return
		default:
		}

		m.mu.Lock()
		defer m.mu.Unlock()

		if m.sessions.Load() > 0 {
			return
		}

		m.logTunnelCloseLocked("idle timeout reached, closing SSH tunnel")
		if m.udpRelay != nil {
			m.udpRelay.Close()
			m.udpRelay = nil
		}
		if m.client != nil {
			_ = m.client.Close()
			m.client = nil
		}
	})
}

func (m *Manager) addBytes(n uint64) {
	if n > 0 {
		m.bytesTransferred.Add(n)
	}
}

func (m *Manager) logTunnelClose(message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logTunnelCloseLocked(message)
}

func (m *Manager) logTunnelCloseLocked(message string) {
	if m.tunnelStartedAt.IsZero() {
		m.log.Infof("%s", message)
		return
	}
	uptime := time.Since(m.tunnelStartedAt)
	bytes := m.bytesTransferred.Load()
	m.log.Infof("%s (uptime %s, %s transferred)", message, formatDuration(uptime), formatBytes(bytes))
	m.tunnelStartedAt = time.Time{}
}

func (m *Manager) stopIdleTimerLocked() {
	if m.idleCancel != nil {
		m.idleCancel()
		m.idleCancel = nil
	}
	if m.idleTimer != nil {
		m.idleTimer.Stop()
		m.idleTimer = nil
	}
}

func (m *Manager) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := m.Acquire(ctx); err != nil {
		return nil, err
	}

	client := m.Client()
	if client == nil {
		m.Release()
		return nil, fmt.Errorf("SSH tunnel is not available")
	}

	type dialResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan dialResult, 1)
	go func() {
		conn, err := client.Dial(network, address)
		ch <- dialResult{conn, err}
	}()

	var conn net.Conn
	var err error
	select {
	case <-ctx.Done():
		m.Release()
		return nil, ctx.Err()
	case res := <-ch:
		conn, err = res.conn, res.err
	}

	if err != nil {
		m.Release()
		return nil, err
	}

	return &trackedConn{
		Conn:    conn,
		onClose: m.Release,
		onBytes: m.addBytes,
	}, nil
}

func (m *Manager) UDPRelay() *UDPRelay {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.udpRelay
}

type trackedConn struct {
	net.Conn
	onClose func()
	onBytes func(uint64)
	once    sync.Once
}

func (c *trackedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && c.onBytes != nil {
		c.onBytes(uint64(n))
	}
	return n, err
}

func (c *trackedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 && c.onBytes != nil {
		c.onBytes(uint64(n))
	}
	return n, err
}

type countingConn struct {
	net.Conn
	onBytes func(uint64)
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && c.onBytes != nil {
		c.onBytes(uint64(n))
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 && c.onBytes != nil {
		c.onBytes(uint64(n))
	}
	return n, err
}

func (c *trackedConn) Close() error {
	c.once.Do(c.onClose)
	return c.Conn.Close()
}

func dialSSH(ctx context.Context, cfg *config.Config) (*ssh.Client, error) {
	authMethods, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := buildHostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.SSH.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}

	addr := cfg.SSH.Address()
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%s", describeSSHDialError(addr, err))
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, sshCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s", describeSSHHandshakeError(addr, cfg.SSH.User, err))
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

func describeSSHDialError(addr string, err error) string {
	if err == nil {
		return "unknown dial error"
	}

	if errors.Is(err, context.Canceled) {
		return fmt.Sprintf("connection to %s canceled", addr)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("connection to %s timed out", addr)
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		host := dnsErr.Name
		if host == "" {
			host = addr
		}
		if dnsErr.IsNotFound {
			return fmt.Sprintf("host not found: %s", host)
		}
		if dnsErr.IsTimeout {
			return fmt.Sprintf("DNS lookup timed out for %s", host)
		}
		if dnsErr.IsTemporary {
			return fmt.Sprintf("temporary DNS failure for %s: %v", host, dnsErr.Err)
		}
		return fmt.Sprintf("DNS lookup failed for %s: %v", host, dnsErr.Err)
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return fmt.Sprintf("connection to %s timed out", addr)
		}
		msg := opErr.Err.Error()
		switch {
		case containsFold(msg, "connection refused"):
			return fmt.Sprintf("connection refused by %s", addr)
		case containsFold(msg, "network is unreachable"),
			containsFold(msg, "no route to host"):
			return fmt.Sprintf("network unreachable: %s", addr)
		case containsFold(msg, "i/o timeout"):
			return fmt.Sprintf("connection to %s timed out", addr)
		case containsFold(msg, "no such host"):
			return fmt.Sprintf("host not found: %s", addr)
		}
		return fmt.Sprintf("cannot reach SSH server %s: %v", addr, opErr.Err)
	}

	if containsFold(err.Error(), "no such host") {
		return fmt.Sprintf("host not found: %s", addr)
	}

	return fmt.Sprintf("cannot reach SSH server %s: %v", addr, err)
}

func describeSSHHandshakeError(addr, user string, err error) string {
	msg := err.Error()
	switch {
	case containsFold(msg, "unable to authenticate"),
		containsFold(msg, "no supported methods remain"),
		containsFold(msg, "permission denied"):
		return fmt.Sprintf("SSH authentication failed for user %q at %s: %v", user, addr, err)
	case containsFold(msg, "handshake"):
		return fmt.Sprintf("SSH handshake with %s failed: %v", addr, err)
	default:
		return fmt.Sprintf("SSH handshake with %s failed: %v", addr, err)
	}
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func buildAuthMethods(cfg *config.Config) ([]ssh.AuthMethod, error) {
	switch cfg.SSH.Auth.Method {
	case "key", "private_key":
		keyData, err := os.ReadFile(cfg.SSH.Auth.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	case "password":
		return []ssh.AuthMethod{ssh.Password(cfg.SSH.Auth.Password)}, nil
	default:
		return nil, fmt.Errorf("unsupported SSH auth method: %s", cfg.SSH.Auth.Method)
	}
}

func buildHostKeyCallback(cfg *config.Config) (ssh.HostKeyCallback, error) {
	if !cfg.SSH.HostKey.Verify {
		return ssh.InsecureIgnoreHostKey(), nil
	}

	expected, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cfg.SSH.HostKey.Key))
	if err != nil {
		return nil, fmt.Errorf("parse ssh.host_key.key: %w", err)
	}
	expectedBytes := expected.Marshal()

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(key.Marshal(), expectedBytes) != 1 {
			return fmt.Errorf("ssh host key mismatch for %s: got %s key with fingerprint %s",
				hostname, key.Type(), ssh.FingerprintSHA256(key))
		}
		return nil
	}, nil
}
