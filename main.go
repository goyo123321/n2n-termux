package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	BuildVersion        = "dev"
	DefaultSignalingURL = ""
)

const DefaultVirtualCIDR = "10.64.0.0/24"

// ============================================================
// ★ Termux CA 证书自动检测
// ============================================================
func init() {
	initTermuxCA()
}

func initTermuxCA() {
	if os.Getenv("SSL_CERT_FILE") != "" {
		return
	}

	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		prefix = os.Getenv("TERMUX_PREFIX")
	}
	if prefix == "" {
		prefix = "/data/data/com.termux/files/usr"
	}

	candidates := []string{
		prefix + "/etc/tls/cert.pem",
		prefix + "/etc/ca-certificates/ca-bundle.crt",
		prefix + "/etc/ssl/certs/ca-certificates.crt",
		prefix + "/etc/ssl/cert.pem",
		"/data/data/com.termux/files/usr/etc/tls/cert.pem",
		"/data/data/com.termux/files/usr/etc/ssl/certs/ca-certificates.crt",
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
	}

	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			_ = os.Setenv("SSL_CERT_FILE", p)
			fmt.Fprintf(os.Stderr, "[TLS] CA 证书: %s (%d bytes)\n", p, st.Size())
			return
		}
	}

	fmt.Fprintf(os.Stderr, "[TLS] ⚠️ 未找到 CA 证书，HTTPS/WSS 可能失败\n")
	fmt.Fprintf(os.Stderr, "[TLS]    修复: pkg install ca-certificates\n")
}

// LAN 缓存
var (
	lanCacheMu   sync.Mutex
	lanCache     []string
	lanCacheTime time.Time
)

func saveLANCache(lans []string) {
	if len(lans) == 0 {
		return
	}
	lanCacheMu.Lock()
	lanCache = append([]string(nil), lans...)
	lanCacheTime = time.Now()
	lanCacheMu.Unlock()
}

func loadLANCache() []string {
	lanCacheMu.Lock()
	defer lanCacheMu.Unlock()
	if len(lanCache) == 0 || time.Since(lanCacheTime) > 5*time.Minute {
		return nil
	}
	return append([]string(nil), lanCache...)
}

type PeerInfo struct {
	ClientID          string
	VirtualIP         string
	PubIP             string
	PubPort           int
	NATType           string
	TurnRelayAddr     string
	UDPAddr           *net.UDPAddr
	lastRecvAt        int64
	lastLANRecvAt     int64
	lastP2PRecvAt     int64 // ★ P2P 直连真实数据帧时间（viaTURN=false 且 isRealData=true）
	loggedReady       bool
	hasRealData       bool
	probeReplied      bool
	AssistedEndpoints []string
	LanEndpoints      []string
	Features          []string
	registeredAt      int64 // peer 首次注册时间，用于 lastLANRecvAt 兜底
}

type Edge struct {
	clientId    string
	nodeName    string
	virtualIP   string
	virtualCIDR string
	roomId      string

	// ★ 网络变化去抖 / 合并延迟（见 edge_network.go）
	lastNetworkChangedAt int64
	lastNetworkType      string
	pendingNetChangeMu   sync.Mutex
	pendingNetChange     string
	pendingNetChangeT    *time.Timer

	serverSeenIP string

	ws         *WSTransport
	relayMgr   *RelayManager
	turnClient *TURNClient

	udpConn *net.UDPConn
	udpPort int

	pktFwd *PacketForwarder

	peers   map[string]*PeerInfo
	peersMu sync.RWMutex

	natMeta *NATMetadata

	fallbackTimers   map[string]*time.Timer
	fallbackTimersMu sync.Mutex

	p2pAvailTimers   map[string]*time.Timer
	p2pAvailTimersMu sync.Mutex

	lastNoPeerLog   map[string]int64
	lastNoPeerLogMu sync.Mutex

	tunStateLog   map[string]ConnType
	tunStateLogMu sync.Mutex

	lastMetadataReportAt int64
	lastDataFrameAt      int64

	// ★ 修复：bool → int，状态变化时重新上报
	p2pStatusReported   map[string]int
	p2pStatusReportedMu sync.Mutex

	streamMgr  *AppStreamManager
	p2pStreams *P2PStreamManager
	socks5Srv  *Socks5Server

	punchAckLimiter *punchAckLimiter

	doneCh  chan struct{}
	closeMu sync.Mutex
	closed  bool
	mu      sync.Mutex
}

func (e *Edge) Done() <-chan struct{} { return e.doneCh }

// ============ UDP 保活 ============

const keepaliveInterval = 5 * time.Second

var keepaliveServers = []string{
	"74.125.250.129:19302",
	"74.125.204.127:19302",
	"162.159.207.0:3478",
	"stun.miwifi.com:3478",
	"stun.cloudflare.com:3478",
}

func buildSTUNBindingRequest() []byte {
	buf := make([]byte, 20)
	buf[0] = 0x00
	buf[1] = 0x01
	buf[2] = 0x00
	buf[3] = 0x00
	buf[4] = 0x21
	buf[5] = 0x12
	buf[6] = 0xA4
	buf[7] = 0x42
	if _, err := rand.Read(buf[8:20]); err != nil {
		binary.BigEndian.PutUint64(buf[8:16], uint64(time.Now().UnixNano()))
	}
	return buf
}

func (e *Edge) startKeepalive() {
	if e.udpConn == nil {
		return
	}

	var addrs []*net.UDPAddr
	for _, s := range keepaliveServers {
		if a, err := net.ResolveUDPAddr("udp4", s); err == nil {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		log.Printf("[Keepalive] ⚠️ 无可用 STUN 服务器，跳过保活")
		return
	}

	log.Printf("[Keepalive] 启动，每 %v 刷新 %d 个 STUN 服务器（自适应：有数据时跳过）",
		keepaliveInterval, len(addrs))

	safeGo("keepalive", func() {
		sendOnce := func() {
			last := atomic.LoadInt64(&e.lastDataFrameAt)
			if last > 0 && time.Now().UnixMilli()-last < 10000 {
				return
			}
			for _, addr := range addrs {
				probe := buildSTUNBindingRequest()
				_, _ = e.udpConn.WriteToUDP(probe, addr)
			}
		}

		sendOnce()

		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.doneCh:
				log.Printf("[Keepalive] 已停止")
				return
			case <-ticker.C:
				sendOnce()
			}
		}
	})
}

// ============ P2P 通道保活 + 活性检测 ============

const p2pKeepaliveInterval     = 15 * time.Second
const p2pIdleKeepaliveInterval = 2 * time.Second
const p2pIdleDeadMs            = 30000
// ★ LAN 状态专用
const lanIdleDeadMs      = 60000
const lanProbeIntervalMs = 30000

func (e *Edge) startP2PKeepalive() {
	log.Printf("[P2P-Keepalive] 启动：非 P2P 通道每 %v，P2P 通道每 %v（防 WiFi 省电）",
		p2pKeepaliveInterval, p2pIdleKeepaliveInterval)

	safeGo("p2p-keepalive", func() {
		ticker := time.NewTicker(p2pKeepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.doneCh:
				log.Printf("[P2P-Keepalive] 已停止")
				return
			case <-ticker.C:
				e.sendP2PKeepalive()
			}
		}
	})

	safeGo("p2p-idle-keepalive", func() {
		ticker := time.NewTicker(p2pIdleKeepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.doneCh:
				log.Printf("[P2P-Idle-Keepalive] 已停止")
				return
			case <-ticker.C:
				e.sendP2PIdleKeepalive()
			}
		}
	})
}

// sendP2PKeepalive 15 秒循环：只对非 LAN / 非 P2P peer 发 probe
func (e *Edge) sendP2PKeepalive() {
	if e.relayMgr == nil || e.p2pStreams == nil {
		return
	}

	e.peersMu.RLock()
	var nonP2PPeers []string
	for id, p := range e.peers {
		if p.UDPAddr == nil {
			continue
		}
		st := e.relayMgr.GetState(id)
		// ★ ConnLAN 也跳过（LAN 的 probe 由 sendP2PIdleKeepalive 处理）
		if st == ConnLAN || st == ConnP2P || st == ConnP2PAvailable {
			continue
		}
		nonP2PPeers = append(nonP2PPeers, id)
	}
	e.peersMu.RUnlock()

	for _, id := range nonP2PPeers {
		e.p2pStreams.sendP2PProbe(id)
	}
}

// sendP2PIdleKeepalive 2 秒循环：P2P / LAN 状态发 probe + 无流量降级检测。
//
//   - LAN：30s probe（保活 + 探测），60s 无流量降级到 P2P
//   - P2P：30s 无 P2P 真实数据降级到 TURN/WS（用 lastP2PRecvAt，不用 lastRecvAt）
//   - P2PAvailable：交给 60s 超时处理
func (e *Edge) sendP2PIdleKeepalive() {
	if e.relayMgr == nil || e.p2pStreams == nil {
		return
	}

	now := time.Now().UnixMilli()

	e.peersMu.RLock()
	var deadPeers []string
	var lanDeadPeers []string
	var lanProbePeers []string
	var p2pPeers []string
	for id, p := range e.peers {
		if p.UDPAddr == nil {
			continue
		}
		st := e.relayMgr.GetState(id)

		if st == ConnLAN {
			// ★ LAN 每 30s 发一次 P2P probe（保活 + 探测）
			if p.lastRecvAt > 0 && now-p.lastRecvAt > lanProbeIntervalMs {
				lanProbePeers = append(lanProbePeers, id)
			}
			// ★ LAN 60s 无流量降级到公网 P2P
			if p.lastRecvAt > 0 && now-p.lastRecvAt > lanIdleDeadMs {
				lanDeadPeers = append(lanDeadPeers, id)
			}
			continue
		}

		if st != ConnP2P && st != ConnP2PAvailable {
			continue
		}

		// ConnP2P 用 lastP2PRecvAt 判定；ConnP2PAvailable 交给 60s 超时
		if st == ConnP2P && p.lastP2PRecvAt > 0 && now-p.lastP2PRecvAt > p2pIdleDeadMs {
			log.Printf("[P2P] %s P2P 真实数据 %ds 未更新（lastRecvAt 可能被 TURN/WS 包刷新），判定 P2P 失效",
				id, (now-p.lastP2PRecvAt)/1000)
			deadPeers = append(deadPeers, id)
			continue
		}
		p2pPeers = append(p2pPeers, id)
	}
	e.peersMu.RUnlock()

	// LAN 30s probe
	for _, id := range lanProbePeers {
		e.p2pStreams.sendP2PProbe(id)
	}

	// LAN 60s 降级到 P2P
	for _, id := range lanDeadPeers {
		log.Printf("[LAN] %s 60s 无流量，降级到公网 P2P", id)
		e.relayMgr.DemoteLANToP2P(id, "no-traffic-60s")
	}

	// P2P 30s 降级到 TURN/WS
	for _, id := range deadPeers {
		log.Printf("[P2P] %s %ds 无 P2P 真实数据，判定 P2P 失效，强制降级",
			id, p2pIdleDeadMs/1000)
		e.relayMgr.ForceFallback(id, "no-traffic-30s")
		e.peersMu.Lock()
		if p := e.peers[id]; p != nil {
			p.lastP2PRecvAt = time.Now().UnixMilli()
		}
		e.peersMu.Unlock()
	}

	for _, id := range p2pPeers {
		e.p2pStreams.sendP2PProbe(id)
	}
}

func (e *Edge) getVirtualIPLocked() string {
	e.peersMu.RLock()
	defer e.peersMu.RUnlock()
	return e.virtualIP
}

// ============ 日志节流 ============

func (e *Edge) logNoPeerThrottled(dstIP string, n int) {
	now := time.Now().UnixMilli()
	e.lastNoPeerLogMu.Lock()
	last := e.lastNoPeerLog["__global__"]
	if now-last < 5000 {
		e.lastNoPeerLogMu.Unlock()
		return
	}
	e.lastNoPeerLog["__global__"] = now
	e.lastNoPeerLogMu.Unlock()
	log.Printf("[TUN] 无匹配 peer，dst=%s len=%d", dstIP, n)
}

func (e *Edge) logTunForward(peerID, dstIP string, n, proto int, sent bool, state ConnType) {
	e.tunStateLogMu.Lock()
	prev, existed := e.tunStateLog[peerID]
	if existed && prev == state {
		e.tunStateLogMu.Unlock()
		return
	}
	e.tunStateLog[peerID] = state
	e.tunStateLogMu.Unlock()

	log.Printf("[TUN] → %s dst=%s len=%d proto=%d sent=%v state=%s",
		peerID, dstIP, n, proto, sent, state)
}

func (e *Edge) forgetTunState(peerID string) {
	e.tunStateLogMu.Lock()
	delete(e.tunStateLog, peerID)
	e.tunStateLogMu.Unlock()
}

// ============ 超时降级 ============

func (e *Edge) scheduleFallbackTimer(peerID string) {
	if e.relayMgr == nil || peerID == "" {
		return
	}

	e.fallbackTimersMu.Lock()
	if t, ok := e.fallbackTimers[peerID]; ok {
		t.Stop()
	}
	t := time.AfterFunc(8*time.Second, func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[fallback-timer] panic: %v", r)
			}
		}()

		e.fallbackTimersMu.Lock()
		delete(e.fallbackTimers, peerID)
		e.fallbackTimersMu.Unlock()

		if e.relayMgr == nil {
			return
		}
		state := e.relayMgr.GetState(peerID)
		if state != ConnUnknown {
			return
		}
		log.Printf("[fallback-timer] %s 8s 内未收到打洞指令，主动降级", peerID)
		e.relayMgr.MarkFallback(peerID)
	})
	e.fallbackTimers[peerID] = t
	e.fallbackTimersMu.Unlock()
}

func (e *Edge) cancelFallbackTimer(peerID string) {
	e.fallbackTimersMu.Lock()
	defer e.fallbackTimersMu.Unlock()
	if t, ok := e.fallbackTimers[peerID]; ok {
		t.Stop()
		delete(e.fallbackTimers, peerID)
	}
}

func (e *Edge) scheduleP2PAvailTimeout(peerID string) {
	if e.relayMgr == nil || peerID == "" {
		return
	}

	e.p2pAvailTimersMu.Lock()
	if t, ok := e.p2pAvailTimers[peerID]; ok {
		t.Stop()
	}

	t := time.AfterFunc(60*time.Second, func() {
		e.p2pAvailTimersMu.Lock()
		delete(e.p2pAvailTimers, peerID)
		e.p2pAvailTimersMu.Unlock()

		if e.relayMgr == nil {
			return
		}
		if e.relayMgr.GetState(peerID) == ConnP2PAvailable {
			log.Printf("[P2P] %s P2PAvailable 超时 60s 未验证数据面，强制降级", peerID)
			e.relayMgr.ForceFallback(peerID, "p2p-avail-timeout")
		}
	})
	e.p2pAvailTimers[peerID] = t
	e.p2pAvailTimersMu.Unlock()
}

func (e *Edge) cancelP2PAvailTimeout(peerID string) {
	if peerID == "" {
		return
	}
	e.p2pAvailTimersMu.Lock()
	defer e.p2pAvailTimersMu.Unlock()
	if t, ok := e.p2pAvailTimers[peerID]; ok {
		t.Stop()
		delete(e.p2pAvailTimers, peerID)
	}
}

// ============ 工具函数 ============

func extractIPFromEndpoint(ep string) string {
	if ep == "" {
		return ""
	}
	i := strings.LastIndex(ep, ":")
	if i < 0 {
		return ep
	}
	return ep[:i]
}

// lastIndexByte 查找字节 c 在字符串 s 中最后一次出现的位置。
//
// 与 strings.LastIndexByte 等价，保持与 n2n-android / n2n-go-client
// 一致的签名（nathole_executor.go 里的 extractPortFromEndpoint 依赖它）。
func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// ============ 主函数 ============

func main() {
	setupResolver()

	signalingURL := getEnv("SIGNALING_URL", DefaultSignalingURL)
	roomId := getEnv("ROOM_ID", "default-room")
	clientId := getEnv("CLIENT_ID", "")
	nodeName := getEnv("NODE_NAME", "")
	udpPort := getEnvInt("UDP_PORT", 0)
	stunServers := getEnv("STUN_SERVERS", "")
	connectToken := getEnv("CONNECT_TOKEN", "")
	socks5Listen := getEnv("SOCKS5_LISTEN", "127.0.0.1:1080")

	if signalingURL == "" {
		log.Fatal("请设置 SIGNALING_URL")
	}

	if clientId == "" {
		installDir := getEnv("INSTALL_DIR", filepath.Join(os.Getenv("HOME"), ".n2n-termux"))
		idFile := filepath.Join(installDir, "client_id")
		if b, err := os.ReadFile(idFile); err == nil {
			clientId = strings.TrimSpace(string(b))
			if clientId != "" {
				log.Printf("[配置] 从 %s 复用 CLIENT_ID: %s", idFile, clientId)
			}
		}
		if clientId == "" {
			hostname, _ := os.Hostname()
			clientId = fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano()%1e9)
			_ = os.MkdirAll(installDir, 0700)
			if err := os.WriteFile(idFile, []byte(clientId), 0600); err != nil {
				log.Printf("[配置] 持久化 CLIENT_ID 失败: %v", err)
			} else {
				log.Printf("[配置] 生成并持久化 CLIENT_ID: %s -> %s", clientId, idFile)
			}
		}
	}
	if nodeName == "" {
		hostname, _ := os.Hostname()
		nodeName = hostname
	}

	log.Printf("n2n-termux %s 启动", BuildVersion)
	if connectToken != "" {
		log.Printf("[配置] CONNECT_TOKEN 已设置（%d 字符）", len(connectToken))
	} else {
		log.Printf("[配置] 未设置 CONNECT_TOKEN")
	}

	edge := &Edge{
		clientId:          clientId,
		nodeName:          nodeName,
		roomId:            roomId,
		virtualCIDR:       DefaultVirtualCIDR,
		peers:             make(map[string]*PeerInfo),
		udpPort:           udpPort,
		fallbackTimers:    make(map[string]*time.Timer),
		p2pAvailTimers:    make(map[string]*time.Timer),
		lastNoPeerLog:     make(map[string]int64),
		tunStateLog:       make(map[string]ConnType),
		p2pStatusReported: make(map[string]int), // ★ int
		doneCh:            make(chan struct{}),
	}
	edge.punchAckLimiter = newPunchAckLimiter()
	edge.streamMgr = NewAppStreamManager()
	edge.pktFwd = NewPacketForwarder(edge)

	udpAddr := &net.UDPAddr{IP: net.IPv4zero, Port: udpPort}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil && udpPort != 0 {
		log.Printf("[P2P] 端口 %d 被占用，回退到内核分配", udpPort)
		udpConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	}
	if err != nil {
		log.Fatalf("绑定 UDP 失败: %v", err)
	}
	edge.udpConn = udpConn
	defer udpConn.Close()

	actualPort := udpConn.LocalAddr().(*net.UDPAddr).Port
	edge.udpPort = actualPort
	log.Printf("[P2P] UDP 监听端口 %d", actualPort)

	if lans := collectLANEndpoints(actualPort, ""); len(lans) > 0 {
		saveLANCache(lans)
		log.Printf("[NAT] 启动预收集 LAN: %v", lans)
	} else {
		log.Printf("[NAT] 启动预收集 LAN 为空（可能蜂窝网络，或 WiFi 未就绪）")
	}

	servers := []string{}
	if stunServers != "" {
		servers = splitCSV(stunServers)
	}
	edge.natMeta = probeNAT(actualPort, servers)

	edge.p2pStreams = NewP2PStreamManager(edge)

	ws, err := NewWSTransport(signalingURL, roomId, clientId, connectToken)
	if err != nil {
		log.Fatalf("连接信令失败: %v", err)
	}
	defer ws.Close()
	edge.ws = ws
	log.Printf("已连接信令，Client ID: %s，节点名: %s", clientId, nodeName)
	ws.StartHeartbeat(20 * time.Second)

	// ★ 修复：删除 ws.onReconnect 回调，避免与 _reconnected 消息双重清理
	//
	// 旧版同时有 onReconnect 回调和 _reconnected 消息：
	//   1. readLoop 重连成功 → onReconnect() → ClearAll + reportMetadata
	//   2. readLoop 再发 routeText({"_reconnected"}) → handleSignaling → ClearAll + reportMetadata
	// 导致重复清理和重复上报。
	//
	// 现在只靠 _reconnected 消息触发清理（handleSignaling 里已有完整逻辑，
	// 且多了 network_changed 补发 + 主动 STUN 探测）。

	edge.turnClient = NewTURNClient(signalingURL, connectToken, edge)
	edge.turnClient.onMessage = func(data []byte, addr net.Addr) {
		ua, ok := addr.(*net.UDPAddr)
		if !ok {
			return
		}
		if isP2PFrame(data) {
			edge.handleP2PFrame(ua, true, data)
			return
		}
		if isPunchProbe(data) {
			edge.notePeerProbeTURN(ua)
			return
		}
		if isPunchAck(data) {
			edge.notePeerAckTURN(ua)
			return
		}
		if len(data) > 0 && data[0]>>4 == 4 {
			if edge.pktFwd != nil {
				edge.pktFwd.HandlePacket(data)
			}
			return
		}
	}

	edge.relayMgr = NewRelayManager(ws, edge.turnClient, edge)
	edge.relayMgr.Report(10 * time.Second)

	ws.SetHandlers(
		edge.handleSignaling,
		func(data []byte) {
			if len(data) >= 4 {
				magic := string(data[:4])
				if magic == "N2NW" || magic == "N2NA" {
					edge.handleAppStreamBinary(data)
					return
				}
			}
			if edge.pktFwd != nil {
				edge.pktFwd.HandlePacket(data)
			}
		},
	)

	safeGo("udpReadLoop", func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-edge.doneCh:
					return
				default:
				}
				log.Printf("[P2P] UDP 读错误: %v", err)
				return
			}
			if n < 4 {
				continue
			}

			if buf[0] == 0x01 && buf[1] == 0x01 {
				if ip, port, ok := parseSTUNResponse(buf[:n]); ok {
					edge.addAssistedEndpoint(ip, port)
				}
				continue
			}

			if isP2PFrame(buf[:n]) {
				edge.handleP2PFrame(addr, false, buf[:n])
				continue
			}

			if isPunchProbe(buf[:n]) {
				edge.notePeerProbe(addr)
				continue
			}
			if isPunchAck(buf[:n]) {
				edge.notePeerAck(addr)
				continue
			}

			if buf[0]>>4 == 4 {
				edge.notePeerTraffic(addr)
				if edge.pktFwd != nil {
					edge.pktFwd.HandlePacket(buf[:n])
				}
			}
		}
	})

	edge.startKeepalive()
	edge.startP2PKeepalive()

	// ★ 启动网络变化探测（Termux 没有 ConnectivityManager，10s 定时探测）
	edge.startNetworkMonitor()

	// ★ 修复：SOCKS5 拆成 Listen() / Serve()，端口冲突时立即报错
	//
	// 旧版直接 Serve()，Serve 里 net.Listen 失败时静默 return，
	// 用户看到"启动成功"但 SOCKS5 没起来。
	//
	// Termux 的 SOCKS5 是唯一主动访问入口，端口冲突时 fatal 退出，
	// 避免用户以为启动了但 curl --socks5 不通。
	if socks5Listen != "" {
		edge.socks5Srv = NewSocks5Server(edge, socks5Listen)
		if err := edge.socks5Srv.Listen(); err != nil {
			log.Printf("[SOCKS5] %v", err)
			log.Fatalf("[SOCKS5] SOCKS5 是 Termux 唯一的数据面入口，无法启动")
		}
		safeGo("socks5-server", func() { edge.socks5Srv.Serve() })
	}

	safeGo("turn-init", func() {
		time.Sleep(2 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := edge.turnClient.FetchAndSetup(ctx); err != nil {
			log.Printf("[TURN] 初始化失败: %v（将使用 WS 中继兜底）", err)
		} else {
			log.Printf("[TURN] TURN 中继就绪: %s", edge.turnClient.GetRelayAddr())
			edge.relayMgr.UpgradeRelaysToTURN()
			_ = edge.ws.Send(map[string]interface{}{
				"type":      "turn_relay_info",
				"relayAddr": edge.turnClient.GetRelayAddr(),
			})
		}
	})

	edge.turnClient.StartReconnectLoop()
	edge.startFailCountResetLoop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("正在退出...")
	edge.Stop()
}

func (e *Edge) Stop() {
	e.closeMu.Lock()
	if e.closed {
		e.closeMu.Unlock()
		return
	}
	e.closed = true
	close(e.doneCh)
	e.closeMu.Unlock()

	// ★ 清理 pending network change timer
	e.pendingNetChangeMu.Lock()
	if e.pendingNetChangeT != nil {
		e.pendingNetChangeT.Stop()
		e.pendingNetChangeT = nil
	}
	e.pendingNetChange = ""
	e.pendingNetChangeMu.Unlock()

	e.fallbackTimersMu.Lock()
	for _, t := range e.fallbackTimers {
		t.Stop()
	}
	e.fallbackTimers = make(map[string]*time.Timer)
	e.fallbackTimersMu.Unlock()

	e.p2pAvailTimersMu.Lock()
	for _, t := range e.p2pAvailTimers {
		t.Stop()
	}
	e.p2pAvailTimers = make(map[string]*time.Timer)
	e.p2pAvailTimersMu.Unlock()

	if e.turnClient != nil {
		e.turnClient.Close()
	}
	if e.socks5Srv != nil {
		e.socks5Srv.Close()
	}
	if e.streamMgr != nil {
		e.streamMgr.closeAll()
	}
	if e.p2pStreams != nil {
		e.p2pStreams.closeAll()
	}
	if e.pktFwd != nil {
		e.pktFwd.Stop()
	}
}

func (e *Edge) reportMetadata() {
	if e.ws == nil || e.natMeta == nil {
		return
	}

	e.mu.Lock()
	nm := e.natMeta
	serverSeenIP := e.serverSeenIP
	e.mu.Unlock()

	if serverSeenIP != "" && nm.PublicEndpoint != "" {
		stunIP := extractIPFromEndpoint(nm.PublicEndpoint)
		if stunIP != "" && stunIP != serverSeenIP {
			nm.MultiExit = true
			nm.NATType = "HardNAT"
			nm.Behavior = "BehaviorPortChanged"
		}
	}

	metaPayload := map[string]interface{}{
		"name":               e.nodeName,
		"natType":            nm.NATType,
		"portsDifference":    nm.PortsDifference,
		"regularPortsChange": nm.RegularPortsChange,
		"behavior":           nm.Behavior,
		"p2pEndpoint":        nm.P2PEndpoint,
		"multiExit":          nm.MultiExit,
		"features":           []string{"n2na", "n2nw"},
	}
	if nm.PublicEndpoint != "" {
		metaPayload["publicEndpoint"] = nm.PublicEndpoint
	}
	if len(nm.AllEndpoints) > 0 {
		metaPayload["assistedEndpoints"] = nm.AllEndpoints
	}

	vip := e.getVirtualIPLocked()
	lanEndpoints := collectLANEndpoints(e.udpPort, vip)
	if len(lanEndpoints) > 0 {
		saveLANCache(lanEndpoints)
	} else {
		if cached := loadLANCache(); len(cached) > 0 {
			lanEndpoints = cached
			log.Printf("[NAT] LAN 实时为空，用缓存: %v", cached)
		}
	}
	if len(lanEndpoints) > 0 {
		metaPayload["lanEndpoints"] = lanEndpoints
		log.Printf("[NAT] 上报 LAN 地址: %v", lanEndpoints)
	}

	log.Printf("[信令] 上报 p2p_metadata: natType=%s publicEndpoint=%q multiExit=%v assisted=%d lan=%d features=[n2na,n2nw]",
		nm.NATType, nm.PublicEndpoint, nm.MultiExit, len(nm.AllEndpoints), len(lanEndpoints))

	_ = e.ws.Send(map[string]interface{}{
		"type":    "p2p_metadata",
		"payload": metaPayload,
	})
}

func (e *Edge) retryLANCollection() {
	safeGo("lan-retry", func() {
		for i := 0; i < 20; i++ {
			select {
			case <-e.doneCh:
				return
			case <-time.After(500 * time.Millisecond):
			}

			vip := e.getVirtualIPLocked()
			lans := collectLANEndpoints(e.udpPort, vip)
			if len(lans) > 0 {
				saveLANCache(lans)
				log.Printf("[NAT] ✅ LAN 重试成功（第 %d 次）: %v，重新上报 p2p_metadata", i+1, lans)
				e.reportMetadata()
				return
			}
		}
		log.Printf("[NAT] ⚠️ LAN 重试 20 次仍为空（10s），放弃")
	})
}

func (e *Edge) printNodeSummary() {
	log.Println("")
	log.Println("================= 本机信息 =================")
	log.Printf("  Client ID   : %s", e.clientId)
	log.Printf("  节点名       : %s", e.nodeName)
	log.Printf("  虚拟 IP     : %s", e.getVirtualIPLocked())
	log.Printf("  虚拟网段    : %s", e.virtualCIDR)
	log.Printf("  P2P 端口    : %d", e.udpPort)
	log.Printf("  模式         : SOCKS5 + packet-forwarder（双向互通）")
	log.Println("==========================================")
	log.Println("")
}

func (e *Edge) logPeerReady(peerID string) {
	if peerID == "" {
		return
	}
	e.peersMu.Lock()
	p, ok := e.peers[peerID]
	if !ok || p.loggedReady {
		e.peersMu.Unlock()
		return
	}
	p.loggedReady = true
	vip := p.VirtualIP
	pubIP := p.PubIP
	pubPort := p.PubPort
	clientID := p.ClientID
	lans := p.LanEndpoints
	assisted := p.AssistedEndpoints
	features := append([]string(nil), p.Features...)
	e.peersMu.Unlock()

	log.Println("")
	log.Printf("========== 节点就绪: %s ==========", clientID)
	log.Printf("  虚拟 IP   : %s", vip)
	if pubIP != "" && pubPort > 0 {
		log.Printf("  公网地址  : %s:%d", pubIP, pubPort)
	}
	if len(lans) > 0 {
		log.Printf("  LAN 地址  : %v", lans)
	}
	if len(assisted) > 0 {
		log.Printf("  STUN 端点 : %v", assisted)
	}
	if len(features) > 0 {
		log.Printf("  features  : %v", features)
	}
	log.Println("==========================================")
	log.Println("")
}

func (e *Edge) handleSignaling(msg map[string]interface{}) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[信令] handleSignaling panic: %v", r)
		}
	}()

	t, _ := msg["type"].(string)
	from, _ := msg["from"].(string)

	if t == "_reconnected" {
		log.Printf("[信令] WebSocket 重连成功，重新上报元数据")

		natHoleActiveMu.Lock()
		count := len(failCounts)
		if count > 0 {
			failCounts = make(map[string]int)
		}
		nActive := len(natHoleActive)
		if nActive > 0 {
			natHoleActive = make(map[string]bool)
		}
		natHoleActiveMu.Unlock()
		if count > 0 || nActive > 0 {
			log.Printf("[NAT-HOLE] WS 重连，清空 %d 个失败计数 / %d 个 in-flight 标记",
				count, nActive)
		}

		if e.relayMgr != nil {
			e.relayMgr.ClearAll()
		}
		if e.punchAckLimiter != nil {
			e.punchAckLimiter.clear()
		}
		if e.p2pStreams != nil {
			e.p2pStreams.closeAll()
		}
		if e.streamMgr != nil {
			e.streamMgr.closeAll()
		}

		e.p2pStatusReportedMu.Lock()
		e.p2pStatusReported = make(map[string]int) // ★ int
		e.p2pStatusReportedMu.Unlock()

		e.reportMetadata()
		atomic.StoreInt64(&e.lastMetadataReportAt, time.Now().UnixMilli())

		// ★ 补发 network_changed（防止切换瞬间丢失导致服务端 punchState 残留）
		if e.ws != nil {
			_ = e.ws.Send(map[string]interface{}{
				"type":   "network_changed",
				"reason": "ws-reconnected",
			})
			log.Printf("[Network] WS 重连，补发 network_changed (reason=ws-reconnected)")
		}

		// ★ 主动触发一次 STUN 探测（不等 5s 保活周期）
		safeGo("reconnect-stun-probe", func() {
			time.Sleep(300 * time.Millisecond)
			if e.udpConn == nil {
				return
			}
			sent := 0
			for _, server := range keepaliveServers {
				addr, err := net.ResolveUDPAddr("udp4", server)
				if err != nil {
					continue
				}
				probe := buildSTUNBindingRequest()
				if _, err := e.udpConn.WriteToUDP(probe, addr); err == nil {
					sent++
				}
			}
			log.Printf("[Network] WS 重连，主动触发 STUN 探测（%d 个服务器，不等 5s 保活）", sent)
		})

		return
	}

	switch t {
	case "ready":
		payload, _ := msg["payload"].(map[string]interface{})

		e.peersMu.Lock()
		e.virtualIP, _ = payload["virtualIp"].(string)
		if cidr, ok := payload["virtualNetwork"].(string); ok && cidr != "" {
			e.virtualCIDR = cidr
		}
		vip := e.virtualIP
		cidr := e.virtualCIDR
		e.peersMu.Unlock()

		log.Printf("分配虚拟 IP: %s（网段 %s）", vip, cidr)

		if serverIP, ok := payload["yourPublicIp"].(string); ok && serverIP != "" {
			e.mu.Lock()
			e.serverSeenIP = serverIP
			e.mu.Unlock()
			log.Printf("[信令] 服务端看到的本机出口 IP: %s（WS/TCP 出口，仅参考）", serverIP)
		}

		e.reportMetadata()

		if len(collectLANEndpoints(e.udpPort, vip)) == 0 {
			log.Printf("[NAT] 首次 LAN 收集为空，启动后台重试")
			e.retryLANCollection()
		}

		if peers, ok := payload["peers"].([]interface{}); ok {
			for _, p := range peers {
				pm, ok := p.(map[string]interface{})
				if !ok {
					continue
				}
				pid, _ := pm["id"].(string)
				pip, _ := pm["virtualIp"].(string)
				pubIP, _ := pm["publicIp"].(string)
				pubPort := 0
				if f, ok := pm["publicPort"].(float64); ok {
					pubPort = int(f)
				}
				relayAddr, _ := pm["turnRelayAddr"].(string)
				natType, _ := pm["natType"].(string)

				var assisted []string
				if raw, ok := pm["assistedEndpoints"].([]interface{}); ok {
					for _, v := range raw {
						if s, ok := v.(string); ok && s != "" {
							assisted = append(assisted, s)
						}
					}
				}

				var lanEndpoints []string
				if raw, ok := pm["lanEndpoints"].([]interface{}); ok {
					for _, v := range raw {
						if s, ok := v.(string); ok && s != "" {
							lanEndpoints = append(lanEndpoints, s)
						}
					}
				}

				var features []string
				if raw, ok := pm["features"].([]interface{}); ok {
					for _, v := range raw {
						if s, ok := v.(string); ok && s != "" {
							features = append(features, s)
						}
					}
				}

				if pid == "" || pip == "" {
					continue
				}

				e.maybeResetFailCountForPeer(pid, pubIP, pubPort)

				e.registerPeer(pid, pip, pubIP, pubPort, natType, assisted, lanEndpoints)
				e.peersMu.Lock()
				if pi, ok := e.peers[pid]; ok {
					if relayAddr != "" {
						pi.TurnRelayAddr = relayAddr
					}
					if len(features) > 0 {
						pi.Features = features
					}
				}
				e.peersMu.Unlock()
				e.logPeerReady(pid)
			}
		}

		e.printNodeSummary()

	case "joined":
		payload, _ := msg["payload"].(map[string]interface{})
		if payload == nil {
			return
		}
		pip, _ := payload["virtualIp"].(string)
		pubIP, _ := payload["publicIp"].(string)
		pubPort := 0
		if f, ok := payload["publicPort"].(float64); ok {
			pubPort = int(f)
		}
		relayAddr, _ := payload["turnRelayAddr"].(string)
		natType, _ := payload["natType"].(string)

		var assisted []string
		if raw, ok := payload["assistedEndpoints"].([]interface{}); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok && s != "" {
					assisted = append(assisted, s)
				}
			}
		}

		var lanEndpoints []string
		if raw, ok := payload["lanEndpoints"].([]interface{}); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok && s != "" {
					lanEndpoints = append(lanEndpoints, s)
				}
			}
		}

		var features []string
		if raw, ok := payload["features"].([]interface{}); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok && s != "" {
					features = append(features, s)
				}
			}
		}

		if from != "" && pip != "" {
			e.peersMu.RLock()
			p, exists := e.peers[from]
			alreadyReady := exists && p.loggedReady
			e.peersMu.RUnlock()

			e.maybeResetFailCountForPeer(from, pubIP, pubPort)

			e.registerPeer(from, pip, pubIP, pubPort, natType, assisted, lanEndpoints)
			e.peersMu.Lock()
			if pi, ok := e.peers[from]; ok {
				if relayAddr != "" {
					pi.TurnRelayAddr = relayAddr
				}
				if len(features) > 0 {
					pi.Features = features
				}
			}
			e.peersMu.Unlock()

			if !alreadyReady {
				log.Printf("[信令] 节点上线: %s (vip=%s) features=%v", from, pip, features)
			}
			e.logPeerReady(from)

			if e.p2pStreams != nil && e.relayMgr != nil {
				if e.relayMgr.GetState(from) != ConnP2P {
					targetID := from
					safeGo("p2p-preconnect", func() {
						time.Sleep(200 * time.Millisecond)
						e.p2pStreams.sendP2PProbe(targetID)
						log.Printf("[P2P] 预连接试探 → %s", targetID)
					})
				}
			}
		}

	case "nat_hole_instruction":
		raw, _ := json.Marshal(msg["payload"])
		var instr NatHoleInstruction
		if err := json.Unmarshal(raw, &instr); err != nil {
			log.Printf("[NAT-HOLE] 指令解析失败: %v", err)
			return
		}
		if !e.peerExists(instr.TargetMac) {
			log.Printf("[NAT-HOLE] 忽略指令 target=%s（peer 不在线）", instr.TargetMac)
			return
		}

		if e.relayMgr != nil && e.relayMgr.GetState(instr.TargetMac) == ConnP2P {
			log.Printf("[NAT-HOLE] 跳过指令 target=%s（已 P2P），上报 p2p_status=3",
				instr.TargetMac)
			e.reportP2PStatus(instr.TargetMac, 3)
			return
		}

		e.ensureTargetPeer(&instr)
		e.scheduleFallbackTimer(instr.TargetMac)
		instrCopy := instr
		safeGo("nat-hole", func() { e.runNatHole(&instrCopy) })

	case "force_fallback":
		payload, _ := msg["payload"].(map[string]interface{})
		if payload == nil {
			return
		}
		peersRaw, _ := payload["peers"].([]interface{})
		reason, _ := payload["reason"].(string)
		if reason == "" {
			reason = "server-forced"
		}
		log.Printf("[信令] 收到 force_fallback: %d 个 peer, reason=%s", len(peersRaw), reason)
		for _, pRaw := range peersRaw {
			peerID, _ := pRaw.(string)
			if peerID == "" {
				continue
			}
			if e.relayMgr == nil {
				continue
			}
			if e.relayMgr.GetState(peerID) == ConnP2P {
				continue
			}
			e.relayMgr.ForceFallback(peerID, reason)
		}

	case "turn_peer_info":
		edgeMac, _ := msg["edgeMac"].(string)
		relayAddr, _ := msg["relayAddr"].(string)
		if edgeMac != "" && relayAddr != "" {
			e.peersMu.Lock()
			if p, ok := e.peers[edgeMac]; ok {
				p.TurnRelayAddr = relayAddr
			}
			e.peersMu.Unlock()
			log.Printf("[TURN] 记录 %s 的中继地址: %s", edgeMac, relayAddr)
		}

	case "app_stream_open":
		safeGo("app-stream-open", func() { e.onRemoteStreamOpen(msg) })
	case "app_stream_open_ack":
		e.onRemoteStreamAck(msg)
	case "app_stream_data":
		e.onRemoteStreamData(msg)
	case "app_stream_close":
		e.onRemoteStreamClose(msg)

	case "pong":
		return

	case "connection_status":
		return

	case "peer_network_changed":
		reason, _ := msg["reason"].(string)
		log.Printf("[Network] %s 网络变化 (%s)，重置与其相关的状态", from, reason)

		if reason == "ws-reconnected" {
			e.peersMu.Lock()
			if p := e.peers[from]; p != nil {
				p.lastRecvAt = time.Now().UnixMilli()
			}
			e.peersMu.Unlock()
			log.Printf("[Network] %s ws-reconnected，保留 P2P 状态", from)
			return
		}

		if e.relayMgr != nil {
			e.relayMgr.ClearPeer(from)
		}

		e.peersMu.Lock()
		oldTurnRelay := ""
		oldUDPAddr := ""
		if p := e.peers[from]; p != nil {
			oldTurnRelay = p.TurnRelayAddr
			if p.UDPAddr != nil {
				oldUDPAddr = p.UDPAddr.String()
			}
			p.UDPAddr = nil
			p.lastLANRecvAt = 0
			p.lastRecvAt = 0
			p.lastP2PRecvAt = 0
			p.PubIP = ""
			p.PubPort = 0
			p.TurnRelayAddr = ""
			p.AssistedEndpoints = nil
			p.LanEndpoints = nil
		}
		e.peersMu.Unlock()
		if oldTurnRelay != "" || oldUDPAddr != "" {
			log.Printf("[Network] %s 已清空 TurnRelayAddr=%q UDPAddr=%q",
				from, oldTurnRelay, oldUDPAddr)
		}

		natHoleActiveMu.Lock()
		delete(failCounts, from)
		delete(natHoleActive, from)
		natHoleActiveMu.Unlock()

		e.cancelFallbackTimer(from)
		e.cancelP2PAvailTimeout(from)
		return

	case "left":
		log.Printf("[信令] 节点离开: %s", from)

		if e.relayMgr != nil {
			e.relayMgr.ClearPeer(from)
		}
		if e.punchAckLimiter != nil {
			e.punchAckLimiter.forget(from)
		}

		e.cancelP2PAvailTimeout(from)

		e.p2pStatusReportedMu.Lock()
		delete(e.p2pStatusReported, from)
		e.p2pStatusReportedMu.Unlock()

		natHoleActiveMu.Lock()
		delete(failCounts, from)
		delete(natHoleActive, from)
		natHoleActiveMu.Unlock()

		e.peersMu.Lock()
		delete(e.peers, from)
		e.peersMu.Unlock()

		e.cancelFallbackTimer(from)
		e.lastNoPeerLogMu.Lock()
		delete(e.lastNoPeerLog, from)
		e.lastNoPeerLogMu.Unlock()
		e.forgetTunState(from)
		if e.p2pStreams != nil {
			e.p2pStreams.dropPeer(from)
		}
	}
}

func (e *Edge) maybeResetFailCountForPeer(peerID, newPubIP string, newPubPort int) {
	if peerID == "" || newPubIP == "" || newPubPort <= 0 {
		return
	}

	e.peersMu.RLock()
	oldPubIP := ""
	oldPubPort := 0
	if p, ok := e.peers[peerID]; ok {
		oldPubIP = p.PubIP
		oldPubPort = p.PubPort
	}
	e.peersMu.RUnlock()

	if oldPubIP == "" || (oldPubIP == newPubIP && oldPubPort == newPubPort) {
		return
	}

	natHoleActiveMu.Lock()
	hadCount := failCounts[peerID]
	delete(failCounts, peerID)
	natHoleActiveMu.Unlock()

	if oldPubIP != newPubIP {
		if e.relayMgr != nil {
			e.relayMgr.ClearPeer(peerID)
		}
		log.Printf("[NAT-HOLE] %s 出口 IP 变化 %s → %s，清空连接状态 (was=%d)",
			peerID, oldPubIP, newPubIP, hadCount)
	} else {
		log.Printf("[NAT-HOLE] %s 端口漂移 %s:%d → %s:%d，保留 P2P 状态 (was=%d)",
			peerID, oldPubIP, oldPubPort, newPubIP, newPubPort, hadCount)
	}
}

// maybeResetOwnFailCounts 本机出口 IP 变化时重置失败计数。
//
// ★ 修复：oldIP == "" 时不 return，而是更新 PublicEndpoint。
func (e *Edge) maybeResetOwnFailCounts(newEndpoint string) {
	if newEndpoint == "" {
		return
	}

	newIP := extractIPFromEndpoint(newEndpoint)
	if newIP == "" {
		return
	}

	e.mu.Lock()
	oldEndpoint := ""
	if e.natMeta != nil {
		oldEndpoint = e.natMeta.PublicEndpoint
	}
	e.mu.Unlock()

	oldIP := extractIPFromEndpoint(oldEndpoint)

	if oldIP == newIP {
		return
	}

	e.mu.Lock()
	if e.natMeta != nil {
		e.natMeta.PublicEndpoint = newEndpoint
		e.natMeta.P2PEndpoint = newEndpoint
	}
	e.mu.Unlock()

	if oldIP != "" {
		natHoleActiveMu.Lock()
		count := len(failCounts)
		if count > 0 {
			failCounts = make(map[string]int)
		}
		natHoleActiveMu.Unlock()

		log.Printf("[NAT-HOLE] 本机出口 IP 变化 %s → %s，重置所有失败计数 (count=%d)",
			oldIP, newIP, count)
	} else {
		log.Printf("[NAT-HOLE] 本机出口 IP 首次设置: %s（换网络后）", newIP)
	}
}

func (e *Edge) peerExists(peerID string) bool {
	if peerID == "" {
		return false
	}
	e.peersMu.RLock()
	defer e.peersMu.RUnlock()
	_, ok := e.peers[peerID]
	return ok
}

func (e *Edge) registerPeer(peerID, virtualIP, pubIP string, pubPort int, natType string, assisted, lanEndpoints []string) {
	log.Printf("[registerPeer] %s: 收到 assisted=%d 个 %v lanEndpoints=%d 个 %v",
		peerID, len(assisted), assisted, len(lanEndpoints), lanEndpoints)

	var udpAddr *net.UDPAddr
	if pubIP != "" && pubPort > 0 {
		udpAddr = &net.UDPAddr{IP: net.ParseIP(pubIP), Port: pubPort}
	}

	selfVIP := e.getVirtualIPLocked()
	prefixes := localPrefixes(selfVIP)
	sortedLAN := sortLANByAffinity(lanEndpoints, prefixes)

	log.Printf("[registerPeer] %s: sortedLAN=%v (prefixes=%d)", peerID, sortedLAN, len(prefixes))

	e.peersMu.Lock()
	_, existed := e.peers[peerID]
	if existed {
		p := e.peers[peerID]

		oldPubIP := p.PubIP
		pubIpChanged := oldPubIP != "" && pubIP != "" && oldPubIP != pubIP
		pubIpFirstSet := oldPubIP == "" && pubIP != ""

		needSwitchToPublic := pubIpChanged
		if pubIpFirstSet && udpAddr != nil {
			selfOnLAN := false
			for _, lan := range sortedLAN {
				ra, err := net.ResolveUDPAddr("udp", lan)
				if err != nil || ra == nil || ra.IP == nil {
					continue
				}
				for _, pfx := range prefixes {
					if pfx.Contains(ra.IP) {
						selfOnLAN = true
						break
					}
				}
				if selfOnLAN {
					break
				}
			}
			if !selfOnLAN {
				needSwitchToPublic = true
				log.Printf("[P2P] %s 首次有公网 (pub=%s)，本机不在其 LAN，UDPAddr 切到公网",
					peerID, pubIP)
			} else {
				log.Printf("[P2P] %s 首次有公网 (pub=%s)，但本机在其 LAN 上，保留 LAN",
					peerID, pubIP)
			}
		}

		if virtualIP != "" {
			p.VirtualIP = virtualIP
		}
		if pubIP != "" {
			p.PubIP = pubIP
		}
		if pubPort > 0 {
			p.PubPort = pubPort
		}

		if udpAddr != nil {
			oldIsLAN := p.UDPAddr != nil && isPrivateIPv4(p.UDPAddr.IP)
			if !oldIsLAN || needSwitchToPublic {
				p.UDPAddr = udpAddr
				if pubIpChanged {
					log.Printf("[P2P] %s 出口 IP 变化 %s → %s，UDPAddr 切到新公网: %s",
						peerID, oldPubIP, pubIP, udpAddr)
				}
			}
		}

		if natType != "" {
			p.NATType = natType
		}
		if len(assisted) > 0 {
			p.AssistedEndpoints = assisted
		}
		if len(sortedLAN) > 0 {
			p.LanEndpoints = sortedLAN
		}
	} else {
		var initialUDP *net.UDPAddr
		if len(sortedLAN) > 0 {
			for _, lan := range sortedLAN {
				if ra, err := net.ResolveUDPAddr("udp", lan); err == nil && ra != nil && ra.IP != nil {
					initialUDP = ra
					log.Printf("[P2P] %s 新建 peer，初始 UDPAddr 用 LAN: %s", peerID, lan)
					break
				}
			}
		}
		if initialUDP == nil {
			initialUDP = udpAddr
		}

		e.peers[peerID] = &PeerInfo{
			ClientID:          peerID,
			VirtualIP:         virtualIP,
			PubIP:             pubIP,
			PubPort:           pubPort,
			UDPAddr:           initialUDP,
			NATType:           natType,
			AssistedEndpoints: assisted,
			LanEndpoints:      sortedLAN,
			registeredAt:      time.Now().UnixMilli(),
		}
	}
	e.peersMu.Unlock()

	if !existed {
		e.scheduleFallbackTimer(peerID)
	}
}

func (e *Edge) ensureTargetPeer(instr *NatHoleInstruction) {
	if instr.TargetMac == "" {
		return
	}
	var udpAddr *net.UDPAddr
	if instr.TargetPubSocket != "" {
		udpAddr = parseSockAddr(instr.TargetPubSocket)
	}
	e.peersMu.Lock()
	defer e.peersMu.Unlock()
	if p, ok := e.peers[instr.TargetMac]; ok {
		if udpAddr != nil {
			oldIsLAN := p.UDPAddr != nil && isPrivateIPv4(p.UDPAddr.IP)
			if !oldIsLAN {
				p.UDPAddr = udpAddr
			}
		}
		if instr.TargetVirtualIp != "" {
			p.VirtualIP = instr.TargetVirtualIp
		}
	} else {
		var initialUDP *net.UDPAddr
		if len(instr.TargetLanEndpoints) > 0 {
			for _, lan := range instr.TargetLanEndpoints {
				if ra, err := net.ResolveUDPAddr("udp", lan); err == nil && ra != nil && ra.IP != nil {
					initialUDP = ra
					log.Printf("[P2P] %s 新建 peer（ensureTargetPeer），初始 UDPAddr 用 LAN: %s",
						instr.TargetMac, lan)
					break
				}
			}
		}
		if initialUDP == nil {
			initialUDP = udpAddr
		}

		e.peers[instr.TargetMac] = &PeerInfo{
			ClientID:     instr.TargetMac,
			VirtualIP:    instr.TargetVirtualIp,
			UDPAddr:      initialUDP,
			LanEndpoints: instr.TargetLanEndpoints,
			registeredAt: time.Now().UnixMilli(),
		}
	}
}

func (e *Edge) runNatHole(instr *NatHoleInstruction) {
	res := e.executeNatHole(instr)
	if res == nil {
		return
	}

	if res.State == PunchStateSucceeded {
		e.relayMgr.MarkP2PAvailable(instr.TargetMac)
		safeGo("p2p-probe", func() {
			e.sendP2PProbe(instr.TargetMac)
		})
	} else if res.State == PunchStateFailed {
		e.relayMgr.MarkFallback(instr.TargetMac)
	}

	p2pStatus := 0
	switch e.relayMgr.GetState(instr.TargetMac) {
	case ConnLAN: // ★ 新增
		p2pStatus = 3
	case ConnP2P:
		p2pStatus = 3
	case ConnP2PAvailable:
		p2pStatus = 2
	default:
		p2pStatus = 0
	}

	_ = e.ws.Send(map[string]interface{}{
		"type": "p2p_state_info",
		"payload": map[string]interface{}{
			"to": []map[string]interface{}{
				{
					"macAddr":            instr.TargetMac,
					"observedRaddr":      "",
					"punchResult":        res,
					"punchResultPeerMac": instr.TargetMac,
					"p2pStatus":          p2pStatus,
				},
			},
		},
	})
}

// reportP2PStatus 上报 p2pStatus 给服务端。
//
// ★ 修复：map[string]bool → map[string]int，状态变化时重新上报。
func (e *Edge) reportP2PStatus(peerID string, status int) {
	if e.ws == nil || peerID == "" {
		return
	}

	e.p2pStatusReportedMu.Lock()
	last, exists := e.p2pStatusReported[peerID]
	if exists && last == status {
		e.p2pStatusReportedMu.Unlock()
		return
	}
	e.p2pStatusReported[peerID] = status
	e.p2pStatusReportedMu.Unlock()

	var state int
	var detail string
	switch status {
	case 3:
		state = 3
		detail = "already-p2p"
	case 2:
		state = 1
		detail = "already-p2p-avail"
	case 0:
		state = 2
		detail = "degraded"
	default:
		state = 1
		detail = "unknown-status"
	}

	_ = e.ws.Send(map[string]interface{}{
		"type": "p2p_state_info",
		"payload": map[string]interface{}{
			"to": []map[string]interface{}{
				{
					"macAddr": peerID,
					"punchResult": map[string]interface{}{
						"state":    state,
						"attempts": 0,
						"detail":   detail,
					},
					"punchResultPeerMac": peerID,
					"p2pStatus":          status,
				},
			},
		},
	})
}

func (e *Edge) selfIsOnTargetLAN(target *PeerInfo) bool {
	if target == nil || len(target.LanEndpoints) == 0 {
		return false
	}
	myPrefixes := localPrefixes(e.getVirtualIPLocked())
	if len(myPrefixes) == 0 {
		return false
	}
	for _, lan := range target.LanEndpoints {
		ra, err := net.ResolveUDPAddr("udp", lan)
		if err != nil || ra == nil || ra.IP == nil {
			continue
		}
		for _, p := range myPrefixes {
			if p.Contains(ra.IP) {
				return true
			}
		}
	}
	return false
}

// ============ peer 探测处理 ============

func (e *Edge) notePeerProbe(addr *net.UDPAddr) {
	e.notePeerCommon(addr, false, false, false)
}

func (e *Edge) notePeerAck(addr *net.UDPAddr) {
	e.notePeerCommon(addr, false, false, true)
}

func (e *Edge) notePeerProbeTURN(addr *net.UDPAddr) {
	e.notePeerCommon(addr, false, true, false)
}

func (e *Edge) notePeerAckTURN(addr *net.UDPAddr) {
	e.notePeerCommon(addr, false, true, true)
}

func (e *Edge) notePeerTraffic(addr *net.UDPAddr) {
	e.notePeerCommon(addr, true, false, false)
}

func (e *Edge) notePeerTrafficTURN(addr *net.UDPAddr) {
	e.notePeerCommon(addr, true, true, false)
}

func (e *Edge) notePeerCommon(addr *net.UDPAddr, isRealData bool, viaTURN bool, isAck bool) {
	now := time.Now().UnixMilli()

	e.peersMu.Lock()
	var best *PeerInfo
	bestScore := 0
	addrIP := addr.IP
	addrPort := addr.Port

	for _, p := range e.peers {
		score := 0

		if p.UDPAddr != nil && p.UDPAddr.IP.Equal(addrIP) {
			if p.UDPAddr.Port == addrPort {
				score = 4
			} else {
				score = 3
			}
		}

		for _, lan := range p.LanEndpoints {
			la, err := net.ResolveUDPAddr("udp", lan)
			if err != nil || la == nil || la.IP == nil {
				continue
			}
			if la.IP.Equal(addrIP) {
				if la.Port == addrPort {
					if score < 5 {
						score = 5
					}
				} else if score < 2 {
					score = 2
				}
			}
		}

		for _, ass := range p.AssistedEndpoints {
			aa, err := net.ResolveUDPAddr("udp", ass)
			if err != nil || aa == nil || aa.IP == nil {
				continue
			}
			if aa.IP.Equal(addrIP) {
				if aa.Port == addrPort {
					if score < 4 {
						score = 4
					}
				} else if score < 1 {
					score = 1
				}
			}
		}

		if score > 0 && score > bestScore {
			best = p
			bestScore = score
		}
	}
	if best == nil {
		e.peersMu.Unlock()
		return
	}

	newIsLAN := isPrivateIPv4(addr.IP)
	oldIsLAN := best.UDPAddr != nil && isPrivateIPv4(best.UDPAddr.IP)

	shouldUpdate := false
	if best.UDPAddr == nil {
		shouldUpdate = true
	} else if !oldIsLAN && newIsLAN {
		shouldUpdate = true
	} else if oldIsLAN && !newIsLAN {
		lastLAN := best.lastLANRecvAt
		if lastLAN == 0 {
			lastLAN = best.registeredAt
		}
		if lastLAN > 0 && now-lastLAN > 30000 {
			shouldUpdate = true
			log.Printf("[P2P] %s (note) LAN 无流量（lastLAN=%ds 前），改用公网地址",
				best.ClientID, (now-lastLAN)/1000)
		}
	} else {
		if best.UDPAddr.Port != addrPort {
			shouldUpdate = true
		}
	}

	if newIsLAN {
		best.lastLANRecvAt = now
	}

	if shouldUpdate {
		best.UDPAddr = &net.UDPAddr{IP: addr.IP, Port: addr.Port}
	}

	best.lastRecvAt = now
	// ★ 只有 P2P 直连的真实数据才刷新 lastP2PRecvAt
	if isRealData && !viaTURN {
		best.lastP2PRecvAt = now
	}
	if isRealData {
		best.hasRealData = true
	}
	clientID := best.ClientID
	ip := addr.IP.String()
	e.peersMu.Unlock()

	if e.relayMgr == nil {
		return
	}

	if isRealData {
		// ★ 区分 LAN / 公网 P2P
		if newIsLAN {
			if e.relayMgr.GetState(clientID) != ConnLAN {
				log.Printf("[LAN] 从 %s (%s) 收到真实数据帧 → LAN 直连", clientID, ip)
				e.relayMgr.MarkLAN(clientID)
			}
		} else {
			if e.relayMgr.GetState(clientID) != ConnP2P {
				log.Printf("[P2P] 从 %s (%s) 收到真实数据帧 → P2P 直连", clientID, ip)
				e.relayMgr.MarkP2P(clientID)
			}
		}
		return
	}

	curState := e.relayMgr.GetState(clientID)

	if newIsLAN {
		if curState != ConnLAN {
			kind := "Punch"
			if isAck {
				kind = "ACK"
			}
			log.Printf("[LAN] 从 %s (%s) 收到 %s → LAN 直连", clientID, ip, kind)
			e.relayMgr.MarkLAN(clientID)
		}
	} else {
		justUpgraded := false
		if curState != ConnLAN && curState != ConnP2P && curState != ConnP2PAvailable {
			kind := "Punch"
			if isAck {
				kind = "ACK"
			}
			log.Printf("[P2P] 从 %s (%s) 收到 %s → P2PAvailable（等待数据帧验证）", clientID, ip, kind)
			e.relayMgr.MarkP2PAvailable(clientID)
			justUpgraded = true
		}

		if justUpgraded {
			if e.p2pStreams != nil {
				safeGo("p2p-probe", func() {
					e.p2pStreams.sendP2PProbe(clientID)
				})
			}
			e.scheduleP2PAvailTimeout(clientID)
		}
	}

	if isAck {
		return
	}
	if e.punchAckLimiter == nil || !e.punchAckLimiter.allow(clientID, time.Now()) {
		return
	}

	safeGo("punch-ack", func() {
		ack := buildPunchAck()
		e.writeProbe(addr, ack, viaTURN)
	})
}

func (e *Edge) writeProbe(addr *net.UDPAddr, probe []byte, viaTURN bool) {
	if addr == nil {
		return
	}
	if viaTURN {
		if e.turnClient == nil || !e.turnClient.IsReady() {
			return
		}
		_ = e.turnClient.Send(probe, addr)
		return
	}
	if e.udpConn == nil {
		return
	}
	_, _ = e.udpConn.WriteToUDP(probe, addr)
}

func (e *Edge) sendProbeTo(addr *net.UDPAddr) {
	if e.udpConn == nil || addr == nil {
		return
	}
	probe := buildPunchProbe(e.getVirtualIPLocked())
	e.writeProbe(addr, probe, false)
}

// ============ 多 STUN 端点收集 ============

func (e *Edge) addAssistedEndpoint(ip string, port int) {
	if ip == "" || port <= 0 {
		return
	}
	ep := net.JoinHostPort(ip, strconv.Itoa(port))

	e.mu.Lock()
	if e.natMeta == nil {
		e.mu.Unlock()
		return
	}
	for _, existing := range e.natMeta.AllEndpoints {
		if existing == ep {
			e.mu.Unlock()
			return
		}
	}
	if len(e.natMeta.AllEndpoints) >= 5 {
		e.natMeta.AllEndpoints = e.natMeta.AllEndpoints[1:]
	}
	e.natMeta.AllEndpoints = append(e.natMeta.AllEndpoints, ep)
	count := len(e.natMeta.AllEndpoints)
	oldPublicEndpoint := e.natMeta.PublicEndpoint
	wasEmpty := oldPublicEndpoint == ""
	e.mu.Unlock()

	e.maybeResetOwnFailCounts(ep)

	e.mu.Lock()
	newPubIP := ""
	oldPubIP := extractIPFromEndpoint(oldPublicEndpoint)
	if e.natMeta != nil {
		newPubIP = extractIPFromEndpoint(e.natMeta.PublicEndpoint)
	}
	e.mu.Unlock()

	ipChanged := oldPubIP != "" && newPubIP != "" && oldPubIP != newPubIP

	log.Printf("[NAT] +assisted: %s (total=%d, wasEmpty=%v, ipChanged=%v, %s→%s)",
		ep, count, wasEmpty, ipChanged, oldPubIP, newPubIP)

	if wasEmpty || ipChanged {
		atomic.StoreInt64(&e.lastMetadataReportAt, 0)
		e.reportMetadata()
		if ipChanged {
			log.Printf("[NAT] PublicEndpoint IP 变化 %s → %s，立即上报 p2p_metadata",
				oldPubIP, newPubIP)
		} else {
			log.Printf("[NAT] PublicEndpoint 首次设置，立即上报 p2p_metadata")
		}
	} else {
		e.reportMetadataThrottled()
	}
}

func (e *Edge) reportMetadataThrottled() {
	now := time.Now().UnixMilli()
	last := atomic.LoadInt64(&e.lastMetadataReportAt)
	if now-last < 30000 {
		return
	}
	if !atomic.CompareAndSwapInt64(&e.lastMetadataReportAt, last, now) {
		return
	}
	e.reportMetadata()
}

// ============ 环境变量 / CSV ============

func getEnv(k, fb string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fb
}

func getEnvInt(k string, fb int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		_, _ = fmt.Sscanf(v, "%d", &n)
		return n
	}
	return fb
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// ============ DNS 修复（Termux 专用） ============

func setupResolver() {
	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		prefix = os.Getenv("TERMUX_PREFIX")
	}

	candidates := []string{}
	if prefix != "" {
		candidates = append(candidates, filepath.Join(prefix, "etc", "resolv.conf"))
	}
	candidates = append(candidates,
		"/data/data/com.termux/files/usr/etc/resolv.conf",
		"/etc/resolv.conf",
	)

	var nameservers []string
	var source string
	for _, p := range candidates {
		nss := readNameservers(p)
		if len(nss) > 0 {
			nameservers = nss
			source = p
			break
		}
	}

	if len(nameservers) == 0 {
		nameservers = []string{"8.8.8.8:53", "1.1.1.1:53", "114.114.114.114:53"}
		source = "<fallback>"
	}

	log.Printf("[DNS] 使用 %s 的 %d 个 nameserver: %v", source, len(nameservers), nameservers)

	var idx uint32
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			n := atomic.AddUint32(&idx, 1)
			ns := nameservers[int(n)%len(nameservers)]

			netProto := "udp"
			if strings.HasPrefix(network, "tcp") {
				netProto = "tcp"
			}
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, netProto, ns)
		},
	}
}

func readNameservers(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var result []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			ns := fields[1]
			if strings.Contains(ns, ":") {
				if !strings.HasPrefix(ns, "[") {
					ns = "[" + ns + "]"
				}
				ns += ":53"
			} else {
				ns += ":53"
			}
			result = append(result, ns)
		}
	}
	return result
}

// ============ LAN 地址收集（内联） ============

func collectLANEndpoints(port int, selfVIP string) []string {
	var result []string
	seen := make(map[string]bool)

	add := func(ip4 net.IP) {
		if ip4 == nil {
			return
		}
		if selfVIP != "" && ip4.String() == selfVIP {
			return
		}
		if isCGNATOverlay(ip4) {
			return
		}
		if !isPrivateIPv4(ip4) {
			return
		}
		if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			return
		}
		ep := net.JoinHostPort(ip4.String(), strconv.Itoa(port))
		if seen[ep] {
			return
		}
		seen[ep] = true
		result = append(result, ep)
	}

	if ip := detectOutboundIP(); ip != nil {
		add(ip)
	}

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip == nil || ip.To4() == nil {
					continue
				}
				add(ip.To4())
			}
		}
	}

	return result
}

func detectOutboundIP() net.IP {
	targets := []string{
		"8.8.8.8:80",
		"1.1.1.1:80",
		"114.114.114.114:80",
	}
	for _, t := range targets {
		conn, err := net.Dial("udp4", t)
		if err != nil {
			continue
		}
		localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
		_ = conn.Close()
		if !ok || localAddr == nil || localAddr.IP == nil {
			continue
		}
		ip4 := localAddr.IP.To4()
		if ip4 != nil {
			return ip4
		}
	}
	return nil
}

func isPrivateIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	if ip4[0] == 10 {
		return true
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return true
	}
	if ip4[0] == 192 && ip4[1] == 168 {
		return true
	}
	return false
}

func isCGNATOverlay(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
}

func localPrefixes(selfVIP string) []*net.IPNet {
	var result []*net.IPNet
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ip4 := ipnet.IP.To4()
			if isCGNATOverlay(ip4) {
				continue
			}
			if selfVIP != "" && ip4.String() == selfVIP {
				continue
			}
			if !isPrivateIPv4(ip4) {
				continue
			}
			if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			result = append(result, ipnet)
		}
	}
	return result
}

func sortLANByAffinity(lan []string, prefixes []*net.IPNet) []string {
	if len(lan) < 2 || len(prefixes) == 0 {
		return lan
	}
	type entry struct {
		ep    string
		score int
	}
	scored := make([]entry, len(lan))
	for i, ep := range lan {
		host, _, err := net.SplitHostPort(ep)
		if err != nil {
			host = ep
		}
		ip := net.ParseIP(host)
		score := 0
		if ip != nil && !isCGNATOverlay(ip) {
			for _, p := range prefixes {
				if p.Contains(ip) {
					ones, _ := p.Mask.Size()
					if ones > score {
						score = ones
					}
				}
			}
		}
		scored[i] = entry{ep, score}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	out := make([]string, len(scored))
	for i, s := range scored {
		out[i] = s.ep
	}
	return out
}
