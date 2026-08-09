package udprelay

import (
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const (
	Magic       = "SSR5"
	MaxKeyLen   = 256
	AuthOK      = "OK\n"
)

// Authenticate sends the shared key to the remote UDP relay.
func Authenticate(conn net.Conn, key string) error {
	if len(key) == 0 {
		return fmt.Errorf("udp auth key is empty")
	}
	if len(key) > MaxKeyLen {
		return fmt.Errorf("udp auth key too long")
	}

	buf := make([]byte, 4+2+len(key))
	copy(buf[:4], Magic)
	binary.BigEndian.PutUint16(buf[4:6], uint16(len(key)))
	copy(buf[6:], key)

	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("write auth: %w", err)
	}

	resp := make([]byte, 3)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if subtle.ConstantTimeCompare(resp, []byte(AuthOK)) != 1 {
		return fmt.Errorf("udp relay auth rejected")
	}
	return nil
}
