package main

// 用户态 TCP/IP 栈（gVisor netstack）。
//
// 用途：让 PC / Android（TUN 数据面）可以主动访问 Termux 上的 TCP/UDP 服务。
//
// ★ 限制：
//   1. ICMP 不通（无 root 无法创建 raw socket）
//   2. 二进制大小增加约 5~15 MB
//   3. 编译时间显著增加（gVisor 是巨型库）

import (
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	netstackMTU         = 1400
	netstackChannelSize = 512
	netstackNICID       = 1
)

// NetStack 封装 gVisor netstack。
type NetStack struct {
	s      *stack.Stack
	linkEP *channel.Endpoint
	edge   *Edge

	mu      sync.Mutex
	stopped bool
}

// NewNetStack 创建并初始化 netstack。
//
// vip: Termux 的虚拟 IP（例如 10.64.0.3）
// cidr: 虚拟网段（例如 10.64.0.0/24）
func NewNetStack(edge *Edge, vip, cidr string) (*NetStack, error) {
	ip := net.ParseIP(vip)
	if ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("invalid vip: %s", vip)
	}
	ip4 := ip.To4()

	prefix := 24
	if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
		ones, _ := ipnet.Mask.Size()
		prefix = ones
	}

	ns := &NetStack{
		edge:   edge,
		linkEP: channel.New(netstackChannelSize, netstackMTU, ""),
	}

	ns.s = stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
		},
	})

	if err := ns.s.CreateNIC(netstackNICID, ns.linkEP); err != nil {
		return nil, fmt.Errorf("create nic: %v", err)
	}

	protoAddr := tcpip.ProtocolAddress{
		Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   tcpip.AddrFrom4([4]byte{ip4[0], ip4[1], ip4[2], ip4[3]}),
			PrefixLen: prefix,
		},
	}
	if err := ns.s.AddProtocolAddress(netstackNICID, protoAddr, stack.AddressProperties{}); err != nil {
		return nil, fmt.Errorf("add protocol address: %v", err)
	}

	ns.s.SetRouteTable([]tcpip.Route{
		{
			Destination: header.IPv4EmptySubnet,
			NIC:         netstackNICID,
		},
	})

	ns.startTCPForwarder()
	ns.startUDPForwarder()

	go ns.readLoop()

	log.Printf("[NETSTACK] 已启动 vip=%s cidr=%s prefix=%d", vip, cidr, prefix)
	return ns, nil
}

// readLoop 从 netstack 读包，发回给对端。
//
// 目的 IP = 发起方（PC/Android）的虚拟 IP。通过虚拟 IP 反查 peer，
// 然后用 SendToPeer 发（自动选 LAN/P2P/TURN/WS）。
func (ns *NetStack) readLoop() {
	for {
		pkt := ns.linkEP.Read()
		if pkt == nil {
			return
		}

		data := packetBufferToBytes(pkt)
		pkt.DecRef()

		if len(data) == 0 {
			continue
		}

		ns.sendToPeer(data)
	}
}

// packetBufferToBytes 把 gVisor PacketBuffer 转成字节切片。
func packetBufferToBytes(pkt *stack.PacketBuffer) []byte {
	view := pkt.ToView()
	defer view.Release()
	return view.AsSlice()
}

// sendToPeer 把 netstack 生成的包发给对应的 peer。
func (ns *NetStack) sendToPeer(data []byte) {
	if len(data) < 20 {
		return
	}
	dstIP := net.IP(data[16:20]).String()

	ns.edge.peersMu.RLock()
	var target *PeerInfo
	for _, p := range ns.edge.peers {
		if p.VirtualIP == dstIP {
			target = p
			break
		}
	}
	ns.edge.peersMu.RUnlock()

	if target == nil {
		return
	}

	ns.edge.relayMgr.SendToPeer(target.ClientID, data, target)
}

// InjectPacket 把 PC/Android 发来的裸 IPv4 包喂给 netstack。
func (ns *NetStack) InjectPacket(data []byte) {
	if len(data) < 20 {
		return
	}
	if data[0]>>4 != 4 {
		return
	}
	// 只处理目标 IP 是本机虚拟 IP 的包
	dstIP := net.IP(data[16:20])
	if dstIP.String() != ns.edge.getVirtualIPLocked() {
		return
	}

	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(data),
	})
	ns.linkEP.InjectInbound(ipv4.ProtocolNumber, pkt)
	pkt.DecRef()
}

// Stop 停止 netstack。
func (ns *NetStack) Stop() {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	if ns.stopped {
		return
	}
	ns.stopped = true
	ns.linkEP.Close()
	ns.s.Close()
	ns.s.Wait()
}

// ============ TCP forwarder ============

func (ns *NetStack) startTCPForwarder() {
	forwarder := tcp.NewForwarder(ns.s, 0, 65535, func(r *tcp.ForwarderRequest) {
		go ns.handleTCPConn(r)
	})
	ns.s.SetTransportProtocolHandler(tcp.ProtocolNumber, forwarder.HandlePacket)
}

// handleTCPConn 处理进来的 TCP 连接。
//
// 场景：PC 发 TCP SYN 到 10.64.0.3:80（Termux VIP:80）
//   → netstack 解析 → 触发这个 handler
//   → 我们 dial 127.0.0.1:80
//   → 双向 pipe
func (ns *NetStack) handleTCPConn(r *tcp.ForwarderRequest) {
	id := r.ID()

	localIP := net.IP(id.LocalAddress.AsSlice()).String()
	localPort := int(id.LocalPort)
	remoteIP := net.IP(id.RemoteAddress.AsSlice()).String()
	remotePort := int(id.RemotePort)

	log.Printf("[NETSTACK] TCP %s:%d → %s:%d", remoteIP, remotePort, localIP, localPort)

	// 只处理目标是本机虚拟 IP 的连接（防止循环）
	if localIP != ns.edge.getVirtualIPLocked() {
		r.Complete(true) // 发 RST
		return
	}

	// 连到本地服务
	localAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	localConn, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
	if err != nil {
		log.Printf("[NETSTACK] TCP dial %s 失败: %v", localAddr, err)
		r.Complete(true)
		return
	}

	// 创建 netstack endpoint
	var wq waiter.Queue
	ep, tcpErr := r.CreateEndpoint(&wq)
	r.Complete(false)
	if tcpErr != nil {
		log.Printf("[NETSTACK] TCP CreateEndpoint 失败: %v", tcpErr)
		localConn.Close()
		return
	}

	// 用 gonet 适配成 net.Conn
	gonetConn := gonet.NewTCPConn(&wq, ep)

	// 双向拷贝
	go func() {
		defer gonetConn.Close()
		defer localConn.Close()
		_, _ = io.Copy(gonetConn, localConn)
	}()
	go func() {
		defer gonetConn.Close()
		defer localConn.Close()
		_, _ = io.Copy(localConn, gonetConn)
	}()
}

// ============ UDP forwarder ============

func (ns *NetStack) startUDPForwarder() {
	forwarder := udp.NewForwarder(ns.s, func(r *udp.ForwarderRequest) {
		go ns.handleUDPConn(r)
	})
	ns.s.SetTransportProtocolHandler(udp.ProtocolNumber, forwarder.HandlePacket)
}

// handleUDPConn 处理进来的 UDP 会话。
//
// netstack 为每个 5-tuple 创建独立 endpoint。
func (ns *NetStack) handleUDPConn(r *udp.ForwarderRequest) {
	id := r.ID()

	localIP := net.IP(id.LocalAddress.AsSlice()).String()
	localPort := int(id.LocalPort)
	remoteIP := net.IP(id.RemoteAddress.AsSlice()).String()
	remotePort := int(id.RemotePort)

	log.Printf("[NETSTACK] UDP %s:%d → %s:%d", remoteIP, remotePort, localIP, localPort)

	if localIP != ns.edge.getVirtualIPLocked() {
		return
	}

	var wq waiter.Queue
	ep, udpErr := r.CreateEndpoint(&wq)
	if udpErr != nil {
		log.Printf("[NETSTACK] UDP CreateEndpoint 失败: %v", udpErr)
		return
	}

	localAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	remoteUDPAddr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		ep.Close()
		return
	}

	localConn, err := net.DialUDP("udp", nil, remoteUDPAddr)
	if err != nil {
		log.Printf("[NETSTACK] UDP dial %s 失败: %v", localAddr, err)
		ep.Close()
		return
	}

	gonetConn := gonet.NewUDPConn(&wq, ep)

	go func() {
		defer gonetConn.Close()
		defer localConn.Close()
		_, _ = io.Copy(gonetConn, localConn)
	}()
	go func() {
		defer gonetConn.Close()
		defer localConn.Close()
		_, _ = io.Copy(localConn, gonetConn)
	}()
}
