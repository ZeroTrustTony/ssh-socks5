package socks5

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"ssh-socks5/internal/config"
	"ssh-socks5/internal/logger"
	"ssh-socks5/internal/tunnel"
)

const (
	version5              = 0x05
	authUserPass          = 0x02
	authNoAcceptable      = 0xFF
	cmdConnect            = 0x01
	cmdUDPAssociate       = 0x03
	atypIPv4              = 0x01
	atypDomain            = 0x03
	atypIPv6              = 0x04
	repSuccess            = 0x00
	repGeneralFailure     = 0x01
	repHostUnreachable    = 0x04
	repCmdNotSupported    = 0x07
)

type Server struct {
	cfg     *config.Config
	tunnel  *tunnel.Manager
	log     *logger.Logger
	listener net.Listener
	ready   chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	clients int
}

func NewServer(cfg *config.Config, tm *tunnel.Manager, log *logger.Logger) *Server {
	return &Server{
		cfg:    cfg,
		tunnel: tm,
		log:    log,
		ready:  make(chan struct{}),
	}
}

func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.SOCKS5.Listen)
	if err != nil {
		return fmt.Errorf("listen SOCKS5: %w", err)
	}
	s.listener = ln
	s.log.Infof("ssh-socks5 listening on %s", s.cfg.SOCKS5.Listen)
	close(s.ready)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				if !errors.Is(err, net.ErrClosed) {
					s.log.Errorf("accept error: %v", err)
				}
				continue
			}
		}

		if !s.acquireClientSlot() {
			s.log.Errorf("max clients reached, rejecting connection from %s", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}

		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer s.releaseClientSlot()
			s.handleConn(c)
		}(conn)
	}
}

func (s *Server) Wait() {
	s.wg.Wait()
}

func (s *Server) acquireClientSlot() bool {
	if s.cfg.MaxClients <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients >= s.cfg.MaxClients {
		return false
	}
	s.clients++
	return true
}

func (s *Server) releaseClientSlot() {
	if s.cfg.MaxClients <= 0 {
		return
	}
	s.mu.Lock()
	s.clients--
	s.mu.Unlock()
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	s.log.Debugf("client connected: %s", remote)

	if err := s.authenticate(conn); err != nil {
		s.log.Debugf("auth failed for %s: %v", remote, err)
		return
	}

	for {
		cmd, target, err := readRequest(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Debugf("request error from %s: %v", remote, err)
			}
			return
		}

		switch cmd {
		case cmdConnect:
			s.log.Debugf("TCP CONNECT %s from %s", target, remote)
			s.handleConnect(conn, target)
			return
		case cmdUDPAssociate:
			if !s.cfg.UDP.Enabled {
				s.log.Debugf("UDP ASSOCIATE rejected: UDP is disabled")
				_ = writeReply(conn, repCmdNotSupported, nil)
				return
			}
			s.log.Debugf("UDP ASSOCIATE from %s", remote)
			s.handleUDPAssociate(conn)
			return
		default:
			_ = writeReply(conn, repCmdNotSupported, nil)
			return
		}
	}
}

func (s *Server) authenticate(conn net.Conn) error {
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != version5 {
		return fmt.Errorf("unsupported SOCKS version: %d", buf[0])
	}

	methods := make([]byte, buf[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	supported := false
	for _, m := range methods {
		if m == authUserPass {
			supported = true
			break
		}
	}
	if !supported {
		_, _ = conn.Write([]byte{version5, authNoAcceptable})
		return fmt.Errorf("no acceptable auth method")
	}

	if _, err := conn.Write([]byte{version5, authUserPass}); err != nil {
		return err
	}

	if err := readUserPassAuth(conn, s.cfg.SOCKS5.Username, s.cfg.SOCKS5.Password); err != nil {
		return err
	}

	s.log.Debugf("client authenticated: %s", conn.RemoteAddr())
	return nil
}

func readUserPassAuth(conn net.Conn, expectedUser, expectedPass string) error {
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != 0x01 {
		return fmt.Errorf("unsupported auth subnegotiation version")
	}

	user := make([]byte, buf[1])
	if _, err := io.ReadFull(conn, user); err != nil {
		return err
	}

	var passLen [1]byte
	if _, err := io.ReadFull(conn, passLen[:]); err != nil {
		return err
	}
	pass := make([]byte, passLen[0])
	if _, err := io.ReadFull(conn, pass); err != nil {
		return err
	}

	status := byte(0x00)
	userOK := subtle.ConstantTimeCompare(user, []byte(expectedUser)) == 1
	passOK := subtle.ConstantTimeCompare(pass, []byte(expectedPass)) == 1
	if !userOK || !passOK {
		status = 0x01
	}
	if _, err := conn.Write([]byte{0x01, status}); err != nil {
		return err
	}
	if status != 0x00 {
		return fmt.Errorf("invalid credentials")
	}
	return nil
}

func readRequest(conn net.Conn) (byte, string, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return 0, "", err
	}
	if header[0] != version5 {
		return 0, "", fmt.Errorf("invalid SOCKS version")
	}

	addr, err := readAddr(conn, header[3])
	if err != nil {
		return 0, "", err
	}

	return header[1], addr, nil
}

func readAddr(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case atypIPv4:
		buf := make([]byte, 4+2)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		ip := net.IP(buf[:4])
		port := binary.BigEndian.Uint16(buf[4:])
		return fmt.Sprintf("%s:%d", ip.String(), port), nil
	case atypIPv6:
		buf := make([]byte, 16+2)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		ip := net.IP(buf[:16])
		port := binary.BigEndian.Uint16(buf[16:])
		return fmt.Sprintf("[%s]:%d", ip.String(), port), nil
	case atypDomain:
		var lenBuf [1]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return "", err
		}
		domain := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", err
		}
		var portBuf [2]byte
		if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
			return "", err
		}
		port := binary.BigEndian.Uint16(portBuf[:])
		return fmt.Sprintf("%s:%d", string(domain), port), nil
	default:
		return "", fmt.Errorf("unsupported address type: %d", atyp)
	}
}

func writeReply(conn net.Conn, rep byte, bindAddr net.IP) error {
	resp := []byte{version5, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	if bindAddr != nil {
		if ip4 := bindAddr.To4(); ip4 != nil {
			copy(resp[4:8], ip4)
		} else if len(bindAddr) == net.IPv6len {
			resp[3] = atypIPv6
			resp = append([]byte{version5, rep, 0x00, atypIPv6}, bindAddr...)
			resp = append(resp, 0, 0)
		}
	}
	_, err := conn.Write(resp)
	return err
}

func (s *Server) handleConnect(conn net.Conn, target string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	host, port, err := net.SplitHostPort(target)
	if err != nil {
		_ = writeReply(conn, repGeneralFailure, nil)
		return
	}

	remote, err := s.tunnel.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		s.log.Errorf("dial %s failed: %v", target, err)
		_ = writeReply(conn, repHostUnreachable, nil)
		return
	}
	defer remote.Close()

	if err := writeReply(conn, repSuccess, nil); err != nil {
		return
	}

	if err := relay(conn, remote); err != nil && !errors.Is(err, net.ErrClosed) {
		s.log.Debugf("relay ended for %s: %v", target, err)
	}
}

func (s *Server) handleUDPAssociate(conn net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.tunnel.Acquire(ctx); err != nil {
		_ = writeReply(conn, repGeneralFailure, nil)
		return
	}
	defer s.tunnel.Release()

	relay := s.tunnel.UDPRelay()
	if relay == nil {
		_ = writeReply(conn, repGeneralFailure, nil)
		return
	}

	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		_ = writeReply(conn, repGeneralFailure, nil)
		return
	}
	defer udpConn.Close()

	localIP := conn.LocalAddr().(*net.TCPAddr).IP
	if localIP.IsUnspecified() {
		localIP = net.IPv4(127, 0, 0, 1)
	}
	bindIP := localIP.To4()
	if bindIP == nil {
		bindIP = localIP
	}

	if err := writeUDPReply(conn, repSuccess, bindIP, udpConn.LocalAddr().(*net.UDPAddr).Port); err != nil {
		return
	}

	clientAddr := conn.RemoteAddr().(*net.TCPAddr)
	assocCtx, assocCancel := context.WithCancel(context.Background())
	defer assocCancel()

	var assocWG sync.WaitGroup
	assocWG.Add(2)

	go func() {
		defer assocWG.Done()
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
		assocCancel()
	}()

	go func() {
		defer assocWG.Done()
		s.relayUDP(assocCtx, udpConn, s.tunnel, clientAddr)
	}()

	assocWG.Wait()
}

func writeUDPReply(conn net.Conn, rep byte, ip net.IP, port int) error {
	if ip4 := ip.To4(); ip4 != nil {
		resp := make([]byte, 10)
		resp[0] = version5
		resp[1] = rep
		resp[3] = atypIPv4
		copy(resp[4:8], ip4)
		binary.BigEndian.PutUint16(resp[8:], uint16(port))
		_, err := conn.Write(resp)
		return err
	}

	resp := make([]byte, 22)
	resp[0] = version5
	resp[1] = rep
	resp[3] = atypIPv6
	copy(resp[4:20], ip.To16())
	binary.BigEndian.PutUint16(resp[20:], uint16(port))
	_, err := conn.Write(resp)
	return err
}

func (s *Server) relayUDP(ctx context.Context, udpConn *net.UDPConn, tm *tunnel.Manager, clientAddr *net.TCPAddr) {
	buf := make([]byte, 65535)
	var clientUDP *net.UDPAddr

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = udpConn.SetReadDeadline(time.Now().Add(time.Second))
		n, src, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}

		// Bind association to the first datagram from the TCP client's IP.
		if clientUDP == nil {
			if !src.IP.Equal(clientAddr.IP) {
				s.log.Debugf("UDP ASSOCIATE: ignoring packet from unexpected IP %s", src.IP)
				continue
			}
			clientUDP = &net.UDPAddr{IP: append(net.IP(nil), src.IP...), Port: src.Port}
		} else if !src.IP.Equal(clientUDP.IP) || src.Port != clientUDP.Port {
			s.log.Debugf("UDP ASSOCIATE: ignoring packet from %s (expected %s)", src, clientUDP)
			continue
		}

		targetHost, targetPort, payload, err := parseSOCKS5UDPHeader(buf[:n])
		if err != nil {
			s.log.Debugf("invalid UDP header: %v", err)
			continue
		}

		resp, err := func() ([]byte, error) {
			relay := tm.UDPRelay()
			if relay == nil {
				return nil, fmt.Errorf("UDP relay is not available")
			}
			return relay.Exchange(ctx, targetHost, targetPort, payload, 5*time.Second)
		}()
		if err != nil {
			s.log.Debugf("UDP exchange to %s:%d failed: %v", targetHost, targetPort, err)
			continue
		}

		packet := buildSOCKS5UDPHeader(targetHost, targetPort, resp)
		_, _ = udpConn.WriteToUDP(packet, clientUDP)
	}
}

func parseSOCKS5UDPHeader(data []byte) (host string, port int, payload []byte, err error) {
	if len(data) < 10 {
		return "", 0, nil, fmt.Errorf("packet too short")
	}
	if data[2] != 0x00 {
		return "", 0, nil, fmt.Errorf("fragmented UDP not supported")
	}

	switch data[3] {
	case atypIPv4:
		if len(data) < 10 {
			return "", 0, nil, fmt.Errorf("short IPv4 packet")
		}
		host = net.IP(data[4:8]).String()
		port = int(binary.BigEndian.Uint16(data[8:10]))
		return host, port, data[10:], nil
	case atypIPv6:
		if len(data) < 22 {
			return "", 0, nil, fmt.Errorf("short IPv6 packet")
		}
		host = net.IP(data[4:20]).String()
		port = int(binary.BigEndian.Uint16(data[20:22]))
		return host, port, data[22:], nil
	case atypDomain:
		domainLen := int(data[4])
		if len(data) < 7+domainLen {
			return "", 0, nil, fmt.Errorf("short domain packet")
		}
		host = string(data[5 : 5+domainLen])
		portOff := 5 + domainLen
		port = int(binary.BigEndian.Uint16(data[portOff : portOff+2]))
		return host, port, data[portOff+2:], nil
	default:
		return "", 0, nil, fmt.Errorf("unsupported address type")
	}
}

func buildSOCKS5UDPHeader(host string, port int, payload []byte) []byte {
	var header []byte
	header = append(header, 0x00, 0x00, 0x00)

	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		header = append(header, atypIPv4)
		header = append(header, ip4...)
	} else if ip != nil {
		header = append(header, atypIPv6)
		header = append(header, ip.To16()...)
	} else {
		header = append(header, atypDomain)
		header = append(header, byte(len(host)))
		header = append(header, host...)
	}

	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	header = append(header, portBytes...)
	return append(header, payload...)
}

func relay(dst, src net.Conn) error {
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(dst, src); errCh <- err }()
	go func() { _, err := io.Copy(src, dst); errCh <- err }()
	err := <-errCh
	_ = dst.Close()
	_ = src.Close()
	<-errCh
	return err
}
