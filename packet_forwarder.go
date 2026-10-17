package main

// 轻量 IP 包转发器：TCP 中继 + UDP 转发 + ICMP 响应。
//
// 能力：
//   ✅ PC/Android → Termux 的 TCP（中继到 127.0.0.1:port）
//   ✅ PC/Android → Termux 的 UDP（转发到 127.0.0.1:port）
//   ✅ PC/Android → Termux 的 ICMP Echo Request（响应 ping）
//   ❌ Termux 主动发起 UDP/ICMP（无 root）
//
// 依赖：无（纯 Go 标准库）

import (
	"encoding/binary"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
)

const (
	fwdIdleTimeout    = 5 * time.Minute
	fwdReaperInterval = 30 * time.Second

	tcpMaxPayloadLen = 1024
	tcpDialTimeout   = 5 * time.Second
	tcpRcvWindow     = 65535

	// TCP flags
	tcpFlagFIN = 0x01
	tcpFlagSYN = 0x02
	tcpFlagRST = 0x04
	tcpFlagPSH = 0x08
	tcpFlagACK = 0x10

	// IP 协议号
	protoICMP = 1
	protoTCP  = 6
	protoUDP  = 17
)

// ============ UDP NAT ============

type udpNATKey struct {
	srcIP   [4]byte
	srcPort uint16
	dstPort uint16
}

type udpNATEntry struct {
	localConn  *net.UDPConn
	lastUsed   time.Time
	remoteIP   net.IP
	remotePort uint16
	localPort  uint16
}

// ============ TCP 连接 ============

type tcpConnKey struct {
	clientIP   [4]byte
	clientPort uint16
	serverPort uint16
}

type tcpConn struct {
	key        tcpConnKey
	localConn  net.Conn
	iss        uint32
	sndNxt     uint32
	rcvNxt     uint32
	lastActive time.Time

	established bool
	finRecv     bool
	finSent     bool
	closed      bool

	mu sync.Mutex
}

// ============ PacketForwarder ============

type PacketForwarder struct {
	edge    *Edge
	mu      sync.Mutex
	udpTab  map[udpNATKey]*udpNATEntry
	tcpTab  map[tcpConnKey]*tcpConn
	stopped bool
	stopCh  chan struct{}
}

func NewPacketForwarder(edge *Edge) *PacketForwarder {
	f := &PacketForwarder{
		edge:   edge,
		udpTab: make(map[udpNATKey]*udpNATEntry),
		tcpTab: make(map[tcpConnKey]*tcpConn),
		stopCh: make(chan struct{}),
	}
	go f.reaper()
	log.Printf("[FWD] 已启动（TCP 中继 + UDP 转发 + ICMP 响应）")
	return f
}

// HandlePacket 是入口。
func (f *PacketForwarder) HandlePacket(pkt []byte) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return
	}
	selfVIP := net.ParseIP(f.edge.getVirtualIPLocked())
	if selfVIP == nil {
		return
	}
	selfVIP4 := selfVIP.To4()
	if selfVIP4 == nil {
		return
	}

	dstIP := net.IP(pkt[16:20]).To4()
	if dstIP == nil || !dstIP.Equal(selfVIP4) {
		return
	}

	switch pkt[9] {
	case protoTCP:
		f.handleTCP(pkt)
	case protoUDP:
		f.handleUDP(pkt)
	case protoICMP:
		f.handleICMP(pkt)
	}
}

// ============================================================
// UDP
// ============================================================

func (f *PacketForwarder) handleUDP(pkt []byte) {
	ihl := int(pkt[0]&0x0F) * 4
	if ihl < 20 || len(pkt) < ihl+8 {
		return
	}

	srcIP := net.IP(pkt[12:16]).To4()
	if srcIP == nil {
		return
	}
	srcPort := binary.BigEndian.Uint16(pkt[ihl : ihl+2])
	dstPort := binary.BigEndian.Uint16(pkt[ihl+2 : ihl+4])
	udpLen := int(binary.BigEndian.Uint16(pkt[ihl+4 : ihl+6]))
	if udpLen < 8 || ihl+udpLen > len(pkt) {
		return
	}
	payload := pkt[ihl+8 : ihl+udpLen]

	var key udpNATKey
	copy(key.srcIP[:], srcIP)
	key.srcPort = srcPort
	key.dstPort = dstPort

	f.mu.Lock()
	entry, exists := f.udpTab[key]
	if !exists {
		localAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(dstPort)}
		conn, err := net.DialUDP("udp", nil, localAddr)
		if err != nil {
			f.mu.Unlock()
			log.Printf("[FWD-UDP] dial 127.0.0.1:%d 失败: %v", dstPort, err)
			return
		}
		entry = &udpNATEntry{
			localConn:  conn,
			lastUsed:   time.Now(),
			remoteIP:   append(net.IP(nil), srcIP...),
			remotePort: srcPort,
			localPort:  dstPort,
		}
		f.udpTab[key] = entry
		f.mu.Unlock()
		go f.udpReadFromLocal(entry, key)
		log.Printf("[FWD-UDP] + %s:%d → 127.0.0.1:%d", srcIP, srcPort, dstPort)
	} else {
		entry.lastUsed = time.Now()
		f.mu.Unlock()
	}

	if _, err := entry.localConn.Write(payload); err != nil {
		log.Printf("[FWD-UDP] write 失败: %v", err)
		f.removeUDP(key)
	}
}

func (f *PacketForwarder) udpReadFromLocal(entry *udpNATEntry, key udpNATKey) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[FWD-UDP] readFromLocal panic: %v", r)
		}
	}()

	buf := make([]byte, 65535)
	for {
		select {
		case <-f.stopCh:
			return
		default:
		}

		_ = entry.localConn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := entry.localConn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if time.Since(entry.lastUsed) > fwdIdleTimeout {
					f.removeUDP(key)
					return
				}
				continue
			}
			f.removeUDP(key)
			return
		}

		selfVIP := net.ParseIP(f.edge.getVirtualIPLocked())
		if selfVIP == nil {
			continue
		}

		pkt := buildUDPResponsePacket(selfVIP, entry.localPort, entry.remoteIP, entry.remotePort, buf[:n])
		if pkt != nil {
			f.sendToPeer(entry.remoteIP, pkt)
		}
	}
}

func (f *PacketForwarder) removeUDP(key udpNATKey) {
	f.mu.Lock()
	entry, exists := f.udpTab[key]
	if exists {
		delete(f.udpTab, key)
	}
	f.mu.Unlock()
	if exists && entry != nil {
		_ = entry.localConn.Close()
	}
}

// ============================================================
// ICMP Echo Reply
// ============================================================

func (f *PacketForwarder) handleICMP(pkt []byte) {
	ihl := int(pkt[0]&0x0F) * 4
	if ihl < 20 || len(pkt) < ihl+8 {
		return
	}
	srcIP := net.IP(pkt[12:16]).To4()
	if srcIP == nil {
		return
	}
	if pkt[ihl] != 8 {
		return
	}
	icmpLen := len(pkt) - ihl
	if icmpLen < 8 {
		return
	}

	selfVIP := net.ParseIP(f.edge.getVirtualIPLocked())
	if selfVIP == nil {
		return
	}

	log.Printf("[FWD-ICMP] Echo Request from %s", srcIP)

	reply := buildICMPEchoReply(pkt, selfVIP, srcIP, ihl, icmpLen)
	if reply != nil {
		f.sendToPeer(srcIP, reply)
	}
}

func buildICMPEchoReply(reqPkt []byte, selfIP net.IP, remoteIP net.IP, ihl, icmpLen int) []byte {
	selfIP4 := selfIP.To4()
	remoteIP4 := remoteIP.To4()
	if selfIP4 == nil || remoteIP4 == nil {
		return nil
	}

	newICMP := make([]byte, icmpLen)
	copy(newICMP, reqPkt[ihl:ihl+icmpLen])

	newICMP[0] = 0
	newICMP[1] = 0
	newICMP[2] = 0
	newICMP[3] = 0
	binary.BigEndian.PutUint16(newICMP[2:4], icmpChecksum(newICMP))

	totalLen := 20 + icmpLen
	pkt := make([]byte, totalLen)
	pkt[0] = 0x45
	pkt[1] = 0x00
	binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(pkt[4:6], 0)
	binary.BigEndian.PutUint16(pkt[6:8], 0x4000)
	pkt[8] = 64
	pkt[9] = protoICMP
	copy(pkt[12:16], selfIP4)
	copy(pkt[16:20], remoteIP4)
	binary.BigEndian.PutUint16(pkt[10:12], ipChecksum(pkt[:20]))
	copy(pkt[20:], newICMP)

	return pkt
}

// ============================================================
// TCP 中继
// ============================================================

func (f *PacketForwarder) handleTCP(pkt []byte) {
	ihl := int(pkt[0]&0x0F) * 4
	if ihl < 20 || len(pkt) < ihl+20 {
		return
	}
	srcIP := net.IP(pkt[12:16]).To4()
	if srcIP == nil {
		return
	}

	tcpOff := ihl
	srcPort := binary.BigEndian.Uint16(pkt[tcpOff : tcpOff+2])
	dstPort := binary.BigEndian.Uint16(pkt[tcpOff+2 : tcpOff+4])
	seq := binary.BigEndian.Uint32(pkt[tcpOff+4 : tcpOff+8])
	dataOff := int(pkt[tcpOff+12]>>4) * 4
	flags := pkt[tcpOff+13]
	tcpLen := len(pkt) - tcpOff

	if dataOff < 20 || dataOff > tcpLen {
		return
	}
	payload := pkt[tcpOff+dataOff : tcpOff+tcpLen]

	var key tcpConnKey
	copy(key.clientIP[:], srcIP)
	key.clientPort = srcPort
	key.serverPort = dstPort

	f.mu.Lock()
	conn := f.tcpTab[key]
	f.mu.Unlock()

	if flags&tcpFlagRST != 0 {
		if conn != nil {
			f.removeTCP(key, "got RST")
		}
		return
	}

	if flags&tcpFlagSYN != 0 && conn == nil {
		localAddr := net.JoinHostPort("127.0.0.1", itoa(int(dstPort)))
		localConn, err := net.DialTimeout("tcp", localAddr, tcpDialTimeout)
		if err != nil {
			log.Printf("[FWD-TCP] dial %s 失败: %v", localAddr, err)
			f.sendTCP(srcIP, dstPort, srcPort, 0, seq+1, tcpFlagRST|tcpFlagACK, nil)
			return
		}

		iss := rand.Uint32()
		newConn := &tcpConn{
			key:        key,
			localConn:  localConn,
			iss:        iss,
			sndNxt:     iss + 1,
			rcvNxt:     seq + 1,
			lastActive: time.Now(),
		}
		f.mu.Lock()
		f.tcpTab[key] = newConn
		f.mu.Unlock()

		log.Printf("[FWD-TCP] + %s:%d → 127.0.0.1:%d (iss=%08x, rcv=%08x)",
			srcIP, srcPort, dstPort, iss, seq+1)

		f.sendTCPWithMSS(srcIP, dstPort, srcPort, iss, seq+1,
			tcpFlagSYN|tcpFlagACK, nil)
		return
	}

	if conn == nil {
		f.sendTCP(srcIP, dstPort, srcPort, 0,
			seq+uint32(len(payload)), tcpFlagRST|tcpFlagACK, nil)
		return
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()

	if conn.closed {
		return
	}
	conn.lastActive = time.Now()

	if flags&tcpFlagFIN != 0 {
		conn.rcvNxt = seq + uint32(len(payload)) + 1
		f.sendTCP(srcIP, dstPort, srcPort, conn.sndNxt, conn.rcvNxt, tcpFlagACK, nil)
		conn.finRecv = true
		if tcpConn, ok := conn.localConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		if conn.finSent || !conn.established {
			go f.removeTCP(key, "FIN handshake complete")
		}
		return
	}

	if len(payload) > 0 {
		if seq == conn.rcvNxt {
			if _, err := conn.localConn.Write(payload); err != nil {
				log.Printf("[FWD-TCP] write 失败: %v", err)
				f.sendTCP(srcIP, dstPort, srcPort, conn.sndNxt, conn.rcvNxt,
					tcpFlagRST|tcpFlagACK, nil)
				go f.removeTCP(key, "write fail")
				return
			}
			conn.rcvNxt += uint32(len(payload))
		}
		f.sendTCP(srcIP, dstPort, srcPort, conn.sndNxt, conn.rcvNxt, tcpFlagACK, nil)
	}

	if flags&tcpFlagACK != 0 && !conn.established {
		conn.established = true
		log.Printf("[FWD-TCP] ✓ established %s:%d", srcIP, srcPort)
		go f.tcpReadFromLocal(conn, key)
	}
}

// tcpReadFromLocal 从本地服务读数据，发 TCP 段给 PC。
func (f *PacketForwarder) tcpReadFromLocal(conn *tcpConn, key tcpConnKey) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[FWD-TCP] readFromLocal panic: %v", r)
		}
	}()

	remoteIP := net.IP(conn.key.clientIP[:])

	buf := make([]byte, tcpMaxPayloadLen)
	for {
		select {
		case <-f.stopCh:
			return
		default:
		}

		_ = conn.localConn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := conn.localConn.Read(buf)
		if n > 0 {
			conn.mu.Lock()
			if conn.closed {
				conn.mu.Unlock()
				return
			}
			seq := conn.sndNxt
			conn.sndNxt += uint32(n)
			ackVal := conn.rcvNxt
			conn.lastActive = time.Now()
			conn.mu.Unlock()

			f.sendTCP(remoteIP, conn.key.serverPort, conn.key.clientPort,
				seq, ackVal, tcpFlagACK|tcpFlagPSH, buf[:n])
		}

		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				conn.mu.Lock()
				idle := time.Since(conn.lastActive)
				closed := conn.closed
				conn.mu.Unlock()
				if closed {
					return
				}
				if idle > fwdIdleTimeout {
					go f.removeTCP(key, "idle timeout")
					return
				}
				continue
			}

			conn.mu.Lock()
			if conn.closed || conn.finSent {
				conn.mu.Unlock()
				return
			}
			seq := conn.sndNxt
			conn.sndNxt++
			ackVal := conn.rcvNxt
			conn.finSent = true
			shouldRemove := conn.finRecv
			conn.mu.Unlock()

			log.Printf("[FWD-TCP] ← FIN %s:%d", remoteIP, conn.key.clientPort)
			f.sendTCP(remoteIP, conn.key.serverPort, conn.key.clientPort,
				seq, ackVal, tcpFlagFIN|tcpFlagACK, nil)

			if shouldRemove {
				go f.removeTCP(key, "both FIN")
			}
			return
		}
	}
}

func (f *PacketForwarder) removeTCP(key tcpConnKey, reason string) {
	f.mu.Lock()
	conn, exists := f.tcpTab[key]
	if exists {
		delete(f.tcpTab, key)
	}
	f.mu.Unlock()

	if !exists || conn == nil {
		return
	}

	conn.mu.Lock()
	conn.closed = true
	conn.mu.Unlock()

	_ = conn.localConn.Close()
	log.Printf("[FWD-TCP] - %s:%d (%s)",
		net.IP(key.clientIP[:]), key.clientPort, reason)
}

// ============================================================
// TCP 包发送
// ============================================================

func (f *PacketForwarder) sendTCP(
	clientIP net.IP, srcPort, dstPort uint16,
	seq, ack uint32,
	flags uint8,
	payload []byte,
) {
	f.sendTCPInternal(clientIP, srcPort, dstPort, seq, ack, flags, payload, false)
}

func (f *PacketForwarder) sendTCPWithMSS(
	clientIP net.IP, srcPort, dstPort uint16,
	seq, ack uint32,
	flags uint8,
	payload []byte,
) {
	f.sendTCPInternal(clientIP, srcPort, dstPort, seq, ack, flags, payload, true)
}

func (f *PacketForwarder) sendTCPInternal(
	clientIP net.IP, srcPort, dstPort uint16,
	seq, ack uint32,
	flags uint8,
	payload []byte,
	withMSS bool,
) {
	clientIP4 := clientIP.To4()
	if clientIP4 == nil {
		return
	}

	selfVIP := net.ParseIP(f.edge.getVirtualIPLocked())
	if selfVIP == nil {
		return
	}
	selfVIP4 := selfVIP.To4()
	if selfVIP4 == nil {
		return
	}

	tcpHeaderLen := 20
	if withMSS {
		tcpHeaderLen += 4
	}
	tcpLen := tcpHeaderLen + len(payload)
	totalLen := 20 + tcpLen

	pkt := make([]byte, totalLen)

	pkt[0] = 0x45
	pkt[1] = 0x00
	binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(pkt[4:6], 0)
	binary.BigEndian.PutUint16(pkt[6:8], 0x4000)
	pkt[8] = 64
	pkt[9] = protoTCP
	copy(pkt[12:16], selfVIP4)
	copy(pkt[16:20], clientIP4)
	binary.BigEndian.PutUint16(pkt[10:12], ipChecksum(pkt[:20]))

	tcp := pkt[20:]
	binary.BigEndian.PutUint16(tcp[0:2], srcPort)
	binary.BigEndian.PutUint16(tcp[2:4], dstPort)
	binary.BigEndian.PutUint32(tcp[4:8], seq)
	binary.BigEndian.PutUint32(tcp[8:12], ack)
	dataOff := byte(tcpHeaderLen/4) << 4
	tcp[12] = dataOff
	tcp[13] = flags
	binary.BigEndian.PutUint16(tcp[14:16], tcpRcvWindow)
	binary.BigEndian.PutUint16(tcp[16:18], 0)
	binary.BigEndian.PutUint16(tcp[18:20], 0)

	offset := 20
	if withMSS {
		tcp[offset+0] = 2
		tcp[offset+1] = 4
		binary.BigEndian.PutUint16(tcp[offset+2:offset+4], 1400)
		offset += 4
	}
	if len(payload) > 0 {
		copy(tcp[offset:], payload)
	}

	binary.BigEndian.PutUint16(tcp[16:18], tcpChecksum(selfVIP4, clientIP4, tcp))

	f.sendToPeer(clientIP4, pkt)
}

// ============================================================
// ★ 核心修改：统一发包逻辑
// ============================================================
//
// 优先走 relayMgr（LAN / P2P / TURN）。如果返回 false（不可用/失败），
// 则降级到 ws.SendBinary（Worker 兜底）。
//
// ★ 与 edge-signal / n2n-android 对齐：WS 中继路径发 N2NW 帧，显式携带
//   目标 clientId，让服务端精确路由（多 peer 房间下不广播）。
func (f *PacketForwarder) sendToPeer(dstIP net.IP, pkt []byte) {
	if f.edge == nil || f.edge.relayMgr == nil {
		return
	}
	dstStr := dstIP.String()

	f.edge.peersMu.RLock()
	var target *PeerInfo
	for _, p := range f.edge.peers {
		if p.VirtualIP == dstStr {
			target = p
			break
		}
	}
	f.edge.peersMu.RUnlock()

	if target == nil {
		return
	}

	// 1. 尝试 P2P / LAN / TURN 发送
	sent := f.edge.relayMgr.SendToPeer(target.ClientID, pkt, target)

	// 2. 如果发送失败（状态为 Unknown 或 Relay，或底层报错），走 WS 中继
	if !sent && f.edge.ws != nil {
		// ★ 用 N2NW 帧：显式携带目标 clientId，服务端无需解析 IPv4 头
		frame := buildN2NWFrame(target.ClientID, pkt)
		_ = f.edge.ws.SendBinary(frame)
	}
}

// ============================================================
// 清理
// ============================================================

func (f *PacketForwarder) reaper() {
	t := time.NewTicker(fwdReaperInterval)
	defer t.Stop()
	for {
		select {
		case <-f.stopCh:
			return
		case <-t.C:
		}
		now := time.Now()

		var udpToRemove []udpNATKey
		f.mu.Lock()
		for k, entry := range f.udpTab {
			if now.Sub(entry.lastUsed) > fwdIdleTimeout {
				udpToRemove = append(udpToRemove, k)
			}
		}
		f.mu.Unlock()
		for _, k := range udpToRemove {
			f.removeUDP(k)
		}

		var tcpToRemove []tcpConnKey
		f.mu.Lock()
		for k, conn := range f.tcpTab {
			conn.mu.Lock()
			idle := now.Sub(conn.lastActive)
			conn.mu.Unlock()
			if idle > fwdIdleTimeout {
				tcpToRemove = append(tcpToRemove, k)
			}
		}
		f.mu.Unlock()
		for _, k := range tcpToRemove {
			f.removeTCP(k, "idle timeout")
		}
	}
}

func (f *PacketForwarder) Stop() {
	f.mu.Lock()
	if f.stopped {
		f.mu.Unlock()
		return
	}
	f.stopped = true
	f.mu.Unlock()

	close(f.stopCh)

	f.mu.Lock()
	for _, entry := range f.udpTab {
		_ = entry.localConn.Close()
	}
	for _, conn := range f.tcpTab {
		_ = conn.localConn.Close()
	}
	f.udpTab = make(map[udpNATKey]*udpNATEntry)
	f.tcpTab = make(map[tcpConnKey]*tcpConn)
	f.mu.Unlock()

	log.Printf("[FWD] 已停止")
}

// ============================================================
// IP 包构造 & 校验和
// ============================================================

func buildUDPResponsePacket(srcIP net.IP, srcPort uint16, dstIP net.IP, dstPort uint16, payload []byte) []byte {
	srcIP4 := srcIP.To4()
	dstIP4 := dstIP.To4()
	if srcIP4 == nil || dstIP4 == nil {
		return nil
	}

	udpLen := 8 + len(payload)
	totalLen := 20 + udpLen

	pkt := make([]byte, totalLen)

	pkt[0] = 0x45
	pkt[1] = 0x00
	binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(pkt[4:6], 0)
	binary.BigEndian.PutUint16(pkt[6:8], 0x4000)
	pkt[8] = 64
	pkt[9] = protoUDP
	copy(pkt[12:16], srcIP4)
	copy(pkt[16:20], dstIP4)
	binary.BigEndian.PutUint16(pkt[10:12], ipChecksum(pkt[:20]))

	binary.BigEndian.PutUint16(pkt[20:22], srcPort)
	binary.BigEndian.PutUint16(pkt[22:24], dstPort)
	binary.BigEndian.PutUint16(pkt[24:26], uint16(udpLen))
	copy(pkt[28:], payload)
	binary.BigEndian.PutUint16(pkt[26:28], udpChecksum(srcIP4, dstIP4, pkt[20:]))

	return pkt
}

func ipChecksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(header); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	if len(header)%2 == 1 {
		sum += uint32(header[len(header)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

func udpChecksum(srcIP, dstIP net.IP, udpSegment []byte) uint16 {
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], srcIP)
	copy(pseudo[4:8], dstIP)
	pseudo[8] = 0
	pseudo[9] = protoUDP
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(udpSegment)))

	var sum uint32
	for _, b := range [][]byte{pseudo, udpSegment} {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	result := ^uint16(sum)
	if result == 0 {
		result = 0xFFFF
	}
	return result
}

func tcpChecksum(srcIP, dstIP net.IP, tcpSegment []byte) uint16 {
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], srcIP)
	copy(pseudo[4:8], dstIP)
	pseudo[8] = 0
	pseudo[9] = protoTCP
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(tcpSegment)))

	var sum uint32
	for _, b := range [][]byte{pseudo, tcpSegment} {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

func icmpChecksum(data []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(data); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
