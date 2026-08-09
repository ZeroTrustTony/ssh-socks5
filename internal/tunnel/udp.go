package tunnel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"ssh-socks5/internal/config"
	"ssh-socks5/internal/logger"
	"ssh-socks5/internal/udprelay"
	"golang.org/x/crypto/ssh"
)

type UDPRelay struct {
	session   *ssh.Session
	localConn net.Conn
	closeOnce sync.Once
}

// shellQuote returns a single-quoted POSIX shell argument that cannot break out
// of the quotes (prevents remote command injection via SSH exec).
func shellQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'"'"'`) + `'`
}

func startUDPRelay(ctx context.Context, client *ssh.Client, cfg *config.Config, log *logger.Logger, onBytes func(uint64)) (*UDPRelay, error) {
	remotePath := cfg.UDP.RemotePath
	relayPort := cfg.UDP.Port
	// SSH Session.Start runs through the remote shell; quote every argument.
	startCmd := fmt.Sprintf("%s -port %s -key %s",
		shellQuote(remotePath),
		shellQuote(strconv.Itoa(relayPort)),
		shellQuote(cfg.UDP.AuthKey),
	)

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("create UDP relay session: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}

	log.Debugf("starting remote UDP relay: %s", remotePath)
	if err := session.Start(startCmd); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("start remote UDP relay %q: %w", remotePath, err)
	}

	go drain(stdout)
	go drain(stderr)

	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			_ = session.Close()
			return nil, ctx.Err()
		default:
		}

		conn, err = client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", relayPort))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conn == nil {
		_ = session.Close()
		return nil, fmt.Errorf("connect to remote UDP relay on 127.0.0.1:%d: timeout", relayPort)
	}

	if err := udprelay.Authenticate(conn, cfg.UDP.AuthKey); err != nil {
		_ = conn.Close()
		_ = session.Close()
		return nil, fmt.Errorf("udp relay auth: %w", err)
	}

	if onBytes != nil {
		conn = &countingConn{Conn: conn, onBytes: onBytes}
	}

	relay := &UDPRelay{
		session:   session,
		localConn: conn,
	}

	go func() {
		_ = session.Wait()
		relay.Close()
	}()

	log.Debugf("remote UDP relay is ready on 127.0.0.1:%d", relayPort)
	return relay, nil
}

func (r *UDPRelay) Exchange(ctx context.Context, host string, port int, payload []byte, timeout time.Duration) ([]byte, error) {
	if err := writeFrame(r.localConn, host, port, payload); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	if err := r.localConn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}

	data, err := readFrame(r.localConn)
	if err != nil {
		return nil, err
	}
	_ = r.localConn.SetReadDeadline(time.Time{})
	return data, nil
}

func (r *UDPRelay) Close() error {
	r.closeOnce.Do(func() {
		if r.localConn != nil {
			_ = r.localConn.Close()
		}
		if r.session != nil {
			_ = r.session.Close()
		}
	})
	return nil
}

func writeFrame(w io.Writer, host string, port int, payload []byte) error {
	hostBytes := []byte(host)
	if len(hostBytes) > 255 {
		return fmt.Errorf("host too long")
	}

	headerLen := 1 + len(hostBytes) + 2 + 4
	frame := make([]byte, headerLen+len(payload))
	frame[0] = byte(len(hostBytes))
	copy(frame[1:], hostBytes)
	binary.BigEndian.PutUint16(frame[1+len(hostBytes):], uint16(port))
	binary.BigEndian.PutUint32(frame[1+len(hostBytes)+2:], uint32(len(payload)))
	copy(frame[headerLen:], payload)

	_, err := w.Write(frame)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var hostLen [1]byte
	if _, err := io.ReadFull(r, hostLen[:]); err != nil {
		return nil, err
	}

	hostBuf := make([]byte, hostLen[0])
	if _, err := io.ReadFull(r, hostBuf); err != nil {
		return nil, err
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(r, portBuf[:]); err != nil {
		return nil, err
	}

	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	payloadLen := binary.BigEndian.Uint32(lenBuf[:])
	if payloadLen > 65535 {
		return nil, fmt.Errorf("payload too large")
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}

	return payload, nil
}

func drain(r io.Reader) {
	_, _ = io.Copy(io.Discard, r)
}
