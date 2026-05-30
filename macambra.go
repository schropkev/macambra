package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	socksVersion       = 0x05
	socksCmdConnect    = 0x01
	socksCmdBind       = 0x02
	socksCmdUDPAssoc   = 0x03
	socksCmdTorResolve = 0xF0
	socksCmdTorReverse = 0xF1

	socksAddrIPv4   = 0x01
	socksAddrDomain = 0x03
	socksAddrIPv6   = 0x04

	XS_PROTOCOL_REQUEST  = 0x58533031
	XS_PROTOCOL_RESPONSE = 0x58533032
)

type xsProtocolRequest struct {
	Signature uint32
	Domain    int32
	Type      int32
	Protocol  int32
}

type xsProtocolResponse struct {
	Signature uint32
	Error     int32
}

type udpAssociation struct {
	tcpConn  net.Conn
	udpConn  net.Conn
	relayIP  net.IP
	relayPort uint16
}

var (
	listenAddr        string
	upstreamSocksAddr string

	udsListen  string
	udsConnect string
)

func init() {
	flag.StringVar(
		&listenAddr,
		"l",
		"127.0.0.1:1080",
		"Local address to listen on",
	)

	flag.StringVar(
		&upstreamSocksAddr,
		"f",
		"127.0.0.1:9050",
		"Upstream SOCKS5 server address",
	)

	flag.StringVar(
		&udsListen,
		"uds-listen",
		"",
		"xsocket server for listener sockets",
	)

	flag.StringVar(
		&udsConnect,
		"uds-connect",
		"",
		"xsocket server for outbound sockets",
	)

	flag.Parse()
}

func main() {
	var (
		ln  net.Listener
		err error
	)

	if udsListen != "" {
		ln, err = xsocketListenTCP(udsListen, listenAddr)
	} else {
		ln, err = net.Listen("tcp", listenAddr)
	}

	if err != nil {
		log.Fatal("Failed to listen:", err)
	}

	log.Printf(
		"SOCKS5 relay listening on %s -> forwarding to %s",
		listenAddr,
		upstreamSocksAddr,
	)

	if udsListen != "" {
		log.Printf("listener xsocket: %s", udsListen)
	}

	if udsConnect != "" {
		log.Printf("upstream xsocket: %s", udsConnect)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println("Accept error:", err)
			continue
		}

		go handleClient(conn)
	}
}

func xsocketDialTCP(uds, addr string) (net.Conn, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, err
	}

	family := syscall.AF_INET
	var ip4 [4]byte
	var ip6 [16]byte

	if tcpAddr.IP.To4() != nil {
		copy(ip4[:], tcpAddr.IP.To4())
	} else {
		family = syscall.AF_INET6
		copy(ip6[:], tcpAddr.IP.To16())
	}

	fd, err := xsocket(
		uds,
		family,
		syscall.SOCK_STREAM,
		syscall.IPPROTO_TCP,
	)
	if err != nil {
		return nil, err
	}

	if family == syscall.AF_INET {
		sa := &syscall.SockaddrInet4{
			Port: tcpAddr.Port,
			Addr: ip4,
		}

		if err := syscall.Connect(fd, sa); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	} else {
		sa := &syscall.SockaddrInet6{
			Port: tcpAddr.Port,
			Addr: ip6,
		}

		if err := syscall.Connect(fd, sa); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	}

	file := os.NewFile(uintptr(fd), "xsocket-tcp")
	defer file.Close()

	return net.FileConn(file)
}

func xsocketDialUDP(uds string, addr *net.UDPAddr) (*net.UDPConn, error) {
	family := syscall.AF_INET
	var ip4 [4]byte
	var ip6 [16]byte

	if addr.IP.To4() != nil {
		copy(ip4[:], addr.IP.To4())
	} else {
		family = syscall.AF_INET6
		copy(ip6[:], addr.IP.To16())
	}

	fd, err := xsocket(
		uds,
		family,
		syscall.SOCK_DGRAM,
		syscall.IPPROTO_UDP,
	)
	if err != nil {
		return nil, err
	}

	if family == syscall.AF_INET {
		sa := &syscall.SockaddrInet4{
			Port: addr.Port,
			Addr: ip4,
		}

		if err := syscall.Connect(fd, sa); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	} else {
		sa := &syscall.SockaddrInet6{
			Port: addr.Port,
			Addr: ip6,
		}

		if err := syscall.Connect(fd, sa); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	}

	file := os.NewFile(uintptr(fd), "xsocket-udp")
	defer file.Close()

	conn, err := net.FileConn(file)
	if err != nil {
		return nil, err
	}

	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		conn.Close()
		return nil, errors.New("not UDP conn")
	}

	return udpConn, nil
}

func xsocket(
	uds string,
	domain int,
	xtype int,
	protocol int,
) (int, error) {

	ctrlFd, err := connectUnixSeqpacket(uds)
	if err != nil {
		return -1, err
	}

	defer syscall.Close(ctrlFd)

	req := xsProtocolRequest{
		Signature: XS_PROTOCOL_REQUEST,
		Domain:    int32(domain),
		Type:      int32(xtype &^ syscall.SOCK_CLOEXEC),
		Protocol:  int32(protocol),
	}

	reqbuf := make([]byte, 16)

	binary.BigEndian.PutUint32(reqbuf[0:], req.Signature)
	binary.BigEndian.PutUint32(reqbuf[4:], uint32(req.Domain))
	binary.BigEndian.PutUint32(reqbuf[8:], uint32(req.Type))
	binary.BigEndian.PutUint32(reqbuf[12:], uint32(req.Protocol))

	if err := writeFull(ctrlFd, reqbuf); err != nil {
		return -1, err
	}

	if err := syscall.Shutdown(ctrlFd, syscall.SHUT_WR); err != nil {
		return -1, err
	}

	data := make([]byte, 8)
	oob := make([]byte, syscall.CmsgSpace(4))

	n, oobn, _, _, err :=
		syscall.Recvmsg(
			ctrlFd,
			data,
			oob,
			syscall.MSG_CMSG_CLOEXEC,
		)

	if err != nil {
		return -1, err
	}

	if n < 8 {
		return -1, errors.New("short xsocket response")
	}

	resp := xsProtocolResponse{
		Signature: binary.BigEndian.Uint32(data[0:4]),
		Error:     int32(binary.BigEndian.Uint32(data[4:8])),
	}

	if resp.Signature != XS_PROTOCOL_RESPONSE {
		return -1, errors.New("invalid xsocket signature")
	}

	if resp.Error != 0 {
		return -1, syscall.Errno(resp.Error)
	}

	msgs, err :=
		syscall.ParseSocketControlMessage(oob[:oobn])

	if err != nil {
		return -1, err
	}

	for _, msg := range msgs {
		fds, err := syscall.ParseUnixRights(&msg)
		if err != nil {
			continue
		}

		if len(fds) > 0 {
			return fds[len(fds)-1], nil
		}
	}

	return -1, errors.New("xsocket returned no fd")
}

func connectUnixSeqpacket(path string) (int, error) {
	fd, err := syscall.Socket(
		syscall.AF_UNIX,
		syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC,
		0,
	)

	if err != nil {
		return -1, err
	}

	var sa syscall.SockaddrUnix

	if len(path) > 0 && path[0] == '@' {
		sa.Name = "\x00" + path[1:]
	} else {
		sa.Name = path
	}

	if err := syscall.Connect(fd, &sa); err != nil {
		syscall.Close(fd)
		return -1, err
	}

	return fd, nil
}

func writeFull(fd int, buf []byte) error {
	for len(buf) > 0 {
		n, err := syscall.Write(fd, buf)

		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return err
		}

		buf = buf[n:]
	}

	return nil
}

func xsocketListenTCP(
	uds string,
	addr string,
) (net.Listener, error) {

	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, err
	}

	var (
		fd     int
		domain int
	)

	if tcpAddr.IP != nil &&
		tcpAddr.IP.To4() == nil {

		domain = syscall.AF_INET6
	} else {
		domain = syscall.AF_INET
	}

	fd, err = xsocket(
		uds,
		domain,
		syscall.SOCK_STREAM,
		syscall.IPPROTO_TCP,
	)

	if err != nil {
		return nil, err
	}

	if err := syscall.SetsockoptInt(
		fd,
		syscall.SOL_SOCKET,
		syscall.SO_REUSEADDR,
		1,
	); err != nil {
		syscall.Close(fd)
		return nil, err
	}

	if domain == syscall.AF_INET {
		sa := &syscall.SockaddrInet4{
			Port: tcpAddr.Port,
		}

		if ip4 := tcpAddr.IP.To4(); ip4 != nil {
			copy(sa.Addr[:], ip4)
		}

		if err := syscall.Bind(fd, sa); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	} else {
		sa := &syscall.SockaddrInet6{
			Port: tcpAddr.Port,
		}

		ip16 := tcpAddr.IP.To16()
		if ip16 != nil {
			copy(sa.Addr[:], ip16)
		}

		if err := syscall.Bind(fd, sa); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	}

	if err := syscall.Listen(fd, 128); err != nil {
		syscall.Close(fd)
		return nil, err
	}

	file := os.NewFile(
		uintptr(fd),
		"xsocket-listener",
	)

	defer file.Close()

	return net.FileListener(file)
}

func buildSOCKSUDPRequest(
	target string,
	payload []byte,
) ([]byte, error) {

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}

	port64, err := strconv.ParseUint(
		portStr,
		10,
		16,
	)

	if err != nil {
		return nil, err
	}

	packet := []byte{
		0x00,
		0x00,
		0x00,
	}

	ip := net.ParseIP(host)

	if ip != nil {

		if ip4 := ip.To4(); ip4 != nil {

			packet = append(
				packet,
				socksAddrIPv4,
			)

			packet = append(
				packet,
				ip4...,
			)

		} else {

			packet = append(
				packet,
				socksAddrIPv6,
			)

			packet = append(
				packet,
				ip.To16()...,
			)
		}

	} else {

		packet = append(
			packet,
			socksAddrDomain,
		)

		packet = append(
			packet,
			byte(len(host)),
		)

		packet = append(
			packet,
			[]byte(host)...,
		)
	}

	p := make([]byte, 2)

	binary.BigEndian.PutUint16(
		p,
		uint16(port64),
	)

	packet = append(packet, p...)
	packet = append(packet, payload...)

	return packet, nil
}

func createUDPAssociation() (*udpAssociation, error) {
	upstream, err := dialUpstreamTCP(upstreamSocksAddr)
	if err != nil {
		return nil, err
	}

	if err := socksHandshake(upstream); err != nil {
		upstream.Close()
		return nil, err
	}

	req := []byte{
		socksVersion,
		socksCmdUDPAssoc,
		0x00,
		socksAddrIPv4,
		0, 0, 0, 0,
		0, 0,
	}

	if _, err := upstream.Write(req); err != nil {
		upstream.Close()
		return nil, err
	}

	reply, rep, err := readSocksReply(upstream)
	if err != nil {
		upstream.Close()
		return nil, err
	}

	if rep != 0x00 {
		upstream.Close()
		return nil, errors.New("UDP associate rejected")
	}

	atyp := reply[3]

	var relayIP net.IP
	var relayPort uint16

	switch atyp {

	case socksAddrIPv4:
		relayIP = net.IP(reply[4:8])
		relayPort = binary.BigEndian.Uint16(reply[8:10])

	case socksAddrIPv6:
		relayIP = net.IP(reply[4:20])
		relayPort = binary.BigEndian.Uint16(reply[20:22])

	default:
		upstream.Close()
		return nil, errors.New("unsupported relay address")
	}

	relayAddr := net.JoinHostPort(
		relayIP.String(),
		strconv.Itoa(int(relayPort)),
	)

	udpConn, err := dialUDPRemote(relayAddr)
	if err != nil {
		upstream.Close()
		return nil, err
	}

	return &udpAssociation{
		tcpConn:  upstream,
		udpConn:  udpConn,
		relayIP:  relayIP,
		relayPort: relayPort,
	}, nil
}

func dialUpstreamTCP(addr string) (net.Conn, error) {
	if udsConnect == "" {
		return net.Dial("tcp", addr)
	}

	return xsocketDialTCP(udsConnect, addr)
}

func listenUDPForAssociate(ip net.IP) (*net.UDPConn, error) {
	if udsListen == "" {
		return net.ListenUDP("udp", &net.UDPAddr{
			IP:   ip,
			Port: 0,
		})
	}

	family := syscall.AF_INET

	if ip.To4() == nil {
		family = syscall.AF_INET6
	}

	fd, err := xsocket(
		udsListen,
		family,
		syscall.SOCK_DGRAM,
		syscall.IPPROTO_UDP,
	)

	if err != nil {
		return nil, err
	}

	if family == syscall.AF_INET {
		var sa syscall.SockaddrInet4

		if ip4 := ip.To4(); ip4 != nil {
			copy(sa.Addr[:], ip4)
		}

		if err := syscall.Bind(fd, &sa); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	} else {
		var sa syscall.SockaddrInet6

		if ip16 := ip.To16(); ip16 != nil {
			copy(sa.Addr[:], ip16)
		}

		if err := syscall.Bind(fd, &sa); err != nil {
			syscall.Close(fd)
			return nil, err
		}
	}

	file := os.NewFile(uintptr(fd), "xsocket-udp-associate")
	defer file.Close()

	pc, err := net.FilePacketConn(file)
	if err != nil {
		return nil, err
	}

	udp, ok := pc.(*net.UDPConn)
	if !ok {
		pc.Close()
		return nil, errors.New("not udp")
	}

	return udp, nil
}

func dialUDPRemote(addr string) (net.Conn, error) {
	if udsConnect == "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		network := "udp4"

		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			network = "udp6"
		}

		return net.Dial(network, addr)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}

	return xsocketDialUDP(udsConnect, udpAddr)
}

func handleClient(client net.Conn) {
	defer client.Close()

	buf := make([]byte, 4096)

	if _, err := io.ReadFull(client, buf[:2]); err != nil {
		return
	}

	nmethods := int(buf[1])

	if _, err := io.ReadFull(client, buf[:nmethods]); err != nil {
		return
	}

	client.Write([]byte{socksVersion, 0x00})

	if _, err := io.ReadFull(client, buf[:4]); err != nil {
		return
	}

	cmd := buf[1]
	atyp := buf[3]

	destAddr, err := readAddr(client, atyp)
	if err != nil {
		sendReply(client, 0x08)
		return
	}

	switch cmd {
	case socksCmdConnect, socksCmdBind:
		handleTCPCommand(client, cmd, destAddr)

	case socksCmdUDPAssoc:
		handleUDPAssociate(client)

	case socksCmdTorResolve, socksCmdTorReverse:
		handleTorSpecial(client, cmd, destAddr)

	default:
		sendReply(client, 0x07)
	}
}

func handleTCPCommand(client net.Conn, cmd byte, destAddr []byte) {
	upstream, err := dialUpstreamTCP(upstreamSocksAddr)
	if err != nil {
		sendReply(client, 0x01)
		return
	}
	defer upstream.Close()

	if err := socksHandshake(upstream); err != nil {
		sendReply(client, 0x01)
		return
	}

	req := append([]byte{socksVersion, cmd, 0x00}, destAddr...)

	if _, err := upstream.Write(req); err != nil {
		sendReply(client, 0x01)
		return
	}

	reply, rep, err := readSocksReply(upstream)
	if err != nil {
		sendReply(client, 0x01)
		return
	}

	if _, err := client.Write(reply); err != nil {
		return
	}

    if rep != 0x00 {
    	return
    }
    
    if cmd == socksCmdBind {
    	secondReply, secondRep, err := readSocksReply(upstream)
    	if err != nil {
    		return
    	}
    
    	if _, err := client.Write(secondReply); err != nil {
    		return
    	}
    
    	if secondRep != 0x00 {
    		return
    	}
    }

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(upstream, client)
	}()

	go func() {
		defer wg.Done()
		io.Copy(client, upstream)
	}()

	wg.Wait()
}

func handleTorSpecial(client net.Conn, cmd byte, destAddr []byte) {
	upstream, err := dialUpstreamTCP(upstreamSocksAddr)
	if err != nil {
		sendReply(client, 0x01)
		return
	}
	defer upstream.Close()

	if err := socksHandshake(upstream); err != nil {
		sendReply(client, 0x01)
		return
	}

	req := append([]byte{socksVersion, cmd, 0x00}, destAddr...)

	if _, err := upstream.Write(req); err != nil {
		sendReply(client, 0x01)
		return
	}

	reply, _, err := readSocksReply(upstream)
	if err != nil {
		sendReply(client, 0x01)
		return
	}

	client.Write(reply)
}

func handleUDPAssociate(client net.Conn) {
	tcpLocal := client.LocalAddr().(*net.TCPAddr)

    var bindIP net.IP
    
    if tcpLocal.IP.To4() != nil {
    	bindIP = net.IPv4zero
    } else {
    	bindIP = net.IPv6zero
    }
    
    udpConn, err := listenUDPForAssociate(bindIP)

	if err != nil {
		sendReply(client, 0x01)
		return
	}

	defer udpConn.Close()

	udpAddr := udpConn.LocalAddr().(*net.UDPAddr)

	reply := buildReply(
		0x00,
		udpAddr.IP,
		uint16(udpAddr.Port),
	)

	if _, err := client.Write(reply); err != nil {
		return
	}

	log.Printf("UDP ASSOCIATE established on %s", udpAddr.String())

    assoc, err := createUDPAssociation()
    if err != nil {
    	return
    }
    
    defer assoc.tcpConn.Close()
    defer assoc.udpConn.Close()

	clientDone := make(chan struct{})

	go func() {
		io.Copy(io.Discard, client)
		close(clientDone)
	}()

	buf := make([]byte, 65535)

	for {
		select {
		case <-clientDone:
			return

		default:
		}

		n, srcAddr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		if n < 10 {
			continue
		}


		if buf[2] != 0x00 {
			continue
		}

		atyp := buf[3]

		offset := 4

		var targetAddr string

		switch atyp {
		case socksAddrIPv4:
			if n < offset+4+2 {
				continue
			}

			ip := net.IP(buf[offset : offset+4])
			offset += 4

			port := binary.BigEndian.Uint16(buf[offset : offset+2])
			offset += 2

			targetAddr = net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))

		case socksAddrIPv6:
			if n < offset+16+2 {
				continue
			}

			ip := net.IP(buf[offset : offset+16])
			offset += 16

			port := binary.BigEndian.Uint16(buf[offset : offset+2])
			offset += 2

			targetAddr = net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))

		case socksAddrDomain:
			if n < offset+1 {
				continue
			}

			dlen := int(buf[offset])
			offset++

			if n < offset+dlen+2 {
				continue
			}

			host := string(buf[offset : offset+dlen])
			offset += dlen

			port := binary.BigEndian.Uint16(buf[offset : offset+2])
			offset += 2

			targetAddr = net.JoinHostPort(host, strconv.Itoa(int(port)))

		default:
			continue
		}

		payload := buf[offset:n]

    go proxyUDPDatagram(
    	assoc,
    	udpConn,
    	srcAddr,
    	targetAddr,
    	payload,
    )
	}
}

func proxyUDPDatagram(
	assoc *udpAssociation,
	clientUDP *net.UDPConn,
	clientAddr *net.UDPAddr,
	targetAddr string,
	payload []byte,
) {

	packet, err := buildSOCKSUDPRequest(
		targetAddr,
		payload,
	)

	if err != nil {
		return
	}

	assoc.udpConn.SetDeadline(
		time.Now().Add(30 * time.Second),
	)

	if _, err := assoc.udpConn.Write(packet); err != nil {
		return
	}

	buf := make([]byte, 65535)

	n, err := assoc.udpConn.Read(buf)
	if err != nil {
		return
	}

	clientUDP.WriteToUDP(
		buf[:n],
		clientAddr,
	)
}

func readAddr(r io.Reader, atyp byte) ([]byte, error) {
	buf := make([]byte, 262)

	switch atyp {
	case socksAddrIPv4:
		_, err := io.ReadFull(r, buf[:6])
		return append([]byte{atyp}, buf[:6]...), err

	case socksAddrIPv6:
		_, err := io.ReadFull(r, buf[:18])
		return append([]byte{atyp}, buf[:18]...), err

	case socksAddrDomain:
		if _, err := io.ReadFull(r, buf[:1]); err != nil {
			return nil, err
		}

		dlen := int(buf[0])

		if _, err := io.ReadFull(r, buf[1:1+dlen+2]); err != nil {
			return nil, err
		}

		return append([]byte{atyp, byte(dlen)}, buf[1:1+dlen+2]...), nil
	}

	return nil, io.EOF
}

func socksHandshake(conn net.Conn) error {
	if _, err := conn.Write([]byte{socksVersion, 0x01, 0x00}); err != nil {
		return err
	}

	resp := make([]byte, 2)

	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}

	if resp[1] != 0x00 {
		return io.ErrUnexpectedEOF
	}

	return nil
}

func readSocksReply(conn net.Conn) ([]byte, byte, error) {
	buf := make([]byte, 4096)

	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return nil, 0, err
	}

	rep := buf[1]
	atyp := buf[3]

	reply := append([]byte{}, buf[:4]...)

	switch atyp {
	case socksAddrIPv4:
		io.ReadFull(conn, buf[:6])
		reply = append(reply, buf[:6]...)

	case socksAddrIPv6:
		io.ReadFull(conn, buf[:18])
		reply = append(reply, buf[:18]...)

	case socksAddrDomain:
		io.ReadFull(conn, buf[:1])
		dlen := int(buf[0])

		reply = append(reply, buf[0])

		io.ReadFull(conn, buf[:dlen+2])
		reply = append(reply, buf[:dlen+2]...)
	}

	return reply, rep, nil
}

func buildReply(rep byte, ip net.IP, port uint16) []byte {
	reply := []byte{
		socksVersion,
		rep,
		0x00,
	}

	if ip4 := ip.To4(); ip4 != nil {
		reply = append(reply, socksAddrIPv4)
		reply = append(reply, ip4...)
	} else {
		ip16 := ip.To16()
		if ip16 == nil {
			ip16 = net.IPv6zero
		}

		reply = append(reply, socksAddrIPv6)
		reply = append(reply, ip16...)
	}

	p := make([]byte, 2)
	binary.BigEndian.PutUint16(p, port)

	reply = append(reply, p...)

	return reply
}

func buildUDPResponse(ip net.IP, port uint16, payload []byte) []byte {
	header := []byte{
		0x00, 0x00,
		0x00,
	}

	if ip4 := ip.To4(); ip4 != nil {
		header = append(header, socksAddrIPv4)
		header = append(header, ip4...)
	} else {
		ip16 := ip.To16()
		if ip16 == nil {
			return nil
		}

		header = append(header, socksAddrIPv6)
		header = append(header, ip16...)
	}

	p := make([]byte, 2)
	binary.BigEndian.PutUint16(p, port)

	header = append(header, p...)
	header = append(header, payload...)

	return header
}

func sendReply(conn net.Conn, rep byte) {
	reply := []byte{
		socksVersion,
		rep,
		0x00,
		socksAddrIPv4,
		0, 0, 0, 0,
		0, 0,
	}

	conn.Write(reply)
}

func deadlineNowPlus() (t time.Time) {
	return time.Now().Add(30 * time.Second)
}
