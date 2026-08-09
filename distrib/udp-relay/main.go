package main

import (
	"crypto/subtle"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	authMagic   = "SSR5"
	authOK      = "OK\n"
	maxKeyLen   = 256
	defaultPort = 38473
)

func main() {
	port := flag.Int("port", defaultPort, "TCP listen port on 127.0.0.1")
	key := flag.String("key", "", "shared authentication key (required)")
	flag.Parse()

	if *key == "" {
		fmt.Fprintln(os.Stderr, "error: -key is required")
		os.Exit(1)
	}
	if len(*key) > maxKeyLen {
		fmt.Fprintln(os.Stderr, "error: key too long")
		os.Exit(1)
	}
	if *port <= 0 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "error: invalid port")
		os.Exit(1)
	}

	listenAddr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handle(conn, *key)
	}
}

func handle(conn net.Conn, expectedKey string) {
	defer conn.Close()

	if !authenticate(conn, expectedKey) {
		return
	}

	for {
		host, port, payload, err := readFrame(conn)
		if err != nil {
			return
		}

		target, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, port))
		if err != nil {
			continue
		}

		udpConn, err := net.DialUDP("udp", nil, target)
		if err != nil {
			continue
		}

		_, _ = udpConn.Write(payload)

		_ = udpConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65535)
		n, _, err := udpConn.ReadFromUDP(buf)
		_ = udpConn.Close()
		if err != nil {
			continue
		}

		_ = writeFrame(conn, host, port, buf[:n])
	}
}

func authenticate(conn net.Conn, expectedKey string) bool {
	header := make([]byte, 6)
	if _, err := io.ReadFull(conn, header); err != nil {
		return false
	}
	if string(header[:4]) != authMagic {
		return false
	}

	keyLen := int(binary.BigEndian.Uint16(header[4:6]))
	if keyLen <= 0 || keyLen > maxKeyLen {
		return false
	}

	keyBuf := make([]byte, keyLen)
	if _, err := io.ReadFull(conn, keyBuf); err != nil {
		return false
	}
	if subtle.ConstantTimeCompare(keyBuf, []byte(expectedKey)) != 1 {
		return false
	}

	_, err := conn.Write([]byte(authOK))
	return err == nil
}

var writeMu sync.Mutex

func writeFrame(w io.Writer, host string, port int, payload []byte) error {
	hostBytes := []byte(host)

	writeMu.Lock()
	defer writeMu.Unlock()

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

func readFrame(r io.Reader) (string, int, []byte, error) {
	var hostLen [1]byte
	if _, err := io.ReadFull(r, hostLen[:]); err != nil {
		return "", 0, nil, err
	}

	hostBuf := make([]byte, hostLen[0])
	if _, err := io.ReadFull(r, hostBuf); err != nil {
		return "", 0, nil, err
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(r, portBuf[:]); err != nil {
		return "", 0, nil, err
	}
	port := int(binary.BigEndian.Uint16(portBuf[:]))

	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return "", 0, nil, err
	}
	payloadLen := binary.BigEndian.Uint32(lenBuf[:])
	if payloadLen > 65535 {
		return "", 0, nil, fmt.Errorf("payload too large")
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", 0, nil, err
	}

	return string(hostBuf), port, payload, nil
}
