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

type PeerInfo struct {
	ClientID          string
	VirtualIP         string
	PubIP             string
	PubPort           int
	NATType           string
	TurnRelayAddr     string
	UDPAddr           *net.UDPAddr
	lastRecvAt        int64
	loggedReady       bool
	hasRealData       bool
	probeReplied      bool
	AssistedEndpoints []string
}

type Edge struct {
	clientId    string
	nodeName    string
	virtualIP   string
	virtualCIDR string
	roomId      string

	serverSeenIP string

	ws         *WSTransport
	relayMgr   *RelayManager
	turnClient *TURNClient

	udpConn *net.UDPConn
	udpPort int

	peers   map[string]*PeerInfo
	peersMu sync.RWMutex

	natMeta *NATMetadata

	fallbackTimers   map[string]*time.Timer
	fallbackTimersMu sync.Mutex

	lastMetadataReportAt int64

	streamMgr  *AppStreamManager
	p2pStreams *P2PStreamManager
	socks5Srv  *Socks5Server

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
	"162.159.207.0:3478",
	"stun.miwifi.com:3478",
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

	log.Printf("[Keepalive] 启动，每 %v 刷新 %d 个 STUN 服务器（仅在有 peer 时）",
		keepaliveInterval, len(addrs))

	safeGo("keepalive", func() {
		sendOnce := func() {
			for _, addr := range addrs {
				probe := buildSTUNBindingRequest()
				_, _ = e.udpConn.WriteToUDP(probe, addr)
			}
		}

		hasPeers := func() bool {
			e.peersMu.RLock()
			defer e.peersMu.RUnlock()
			return len(e.peers) > 0
		}

		if hasPeers() {
			sendOnce()
		}

		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.doneCh:
				log.Printf("[Keepalive] 已停止")
				return
			case <-ticker.C:
				if !hasPeers() {
					continue
				}
				sendOnce()
			}
		}
	})
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
		clientId:       clientId,
		nodeName:       nodeName,
		roomId:         roomId,
		virtualCIDR:    DefaultVirtualCIDR,
		peers:          make(map[string]*PeerInfo),
		udpPort:        udpPort,
		fallbackTimers: make(map[string]*time.Timer),
		doneCh:         make(chan struct{}),
		streamMgr:      NewAppStreamManager(),
	}

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

	// ★ 修复：WS 重连时清空所有连接状态
	//
	// 之前只调 reportMetadata()，导致重连后 relayMgr.states[peer] 保留旧的
	// "p2p" 状态。下次 SOCKS5 请求时认为走 P2P，但 UDP 通道可能已失效
	// （CGNAT 端口漂移 / hairpin 单向），N2NS SYN 发不出去，2 秒超时才回落 WS。
	//
	// 修复后：WS 断开 → UDP 通道的 peer 状态不可信 → 清空所有状态，
	// 等服务端重新下发打洞指令后再重新判定。
	ws.onReconnect = func() {
		log.Printf("[信令] WebSocket 重连成功，清空所有连接状态")

		if edge.relayMgr != nil {
			edge.relayMgr.ClearAll()
		}
		if edge.p2pStreams != nil {
			edge.p2pStreams.closeAll()
		}

		if edge.virtualIP != "" {
			edge.reportMetadata()
		}
	}

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
		if len(data) >= 4 &&
			data[0] == 'N' && data[1] == '2' && data[2] == 'N' && data[3] == 'P' {
			edge.notePeerProbeTURN(ua)
			return
		}
	}

	edge.relayMgr = NewRelayManager(ws, edge.turnClient, edge)
	edge.relayMgr.Report(10 * time.Second)

	ws.SetHandlers(
		edge.handleSignaling,
		func(data []byte) {
			// proxy 模式：服务端不会下发 binary；忽略
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

			if buf[0] == 'N' && buf[1] == '2' && buf[2] == 'N' && buf[3] == 'P' {
				edge.notePeerProbe(addr)
				continue
			}
		}
	})

	edge.startKeepalive()

	edge.socks5Srv = NewSocks5Server(edge, socks5Listen)
	safeGo("socks5-server", func() { edge.socks5Srv.Serve() })

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

	e.fallbackTimersMu.Lock()
	for _, t := range e.fallbackTimers {
		t.Stop()
	}
	e.fallbackTimers = make(map[string]*time.Timer)
	e.fallbackTimersMu.Unlock()

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
	}
	if nm.PublicEndpoint != "" {
		metaPayload["publicEndpoint"] = nm.PublicEndpoint
	}
	if len(nm.AllEndpoints) > 0 {
		metaPayload["assistedEndpoints"] = nm.AllEndpoints
	}

	log.Printf("[信令] 上报 p2p_metadata: natType=%s publicEndpoint=%q multiExit=%v assisted=%d",
		nm.NATType, nm.PublicEndpoint, nm.MultiExit, len(nm.AllEndpoints))

	_ = e.ws.Send(map[string]interface{}{
		"type":    "p2p_metadata",
		"payload": metaPayload,
	})
}

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

func (e *Edge) printNodeSummary() {
	log.Println("")
	log.Println("================= 本机信息 =================")
	log.Printf("  Client ID   : %s", e.clientId)
	log.Printf("  节点名       : %s", e.nodeName)
	log.Printf("  虚拟 IP     : %s", e.virtualIP)
	log.Printf("  虚拟网段    : %s", e.virtualCIDR)
	log.Printf("  P2P 端口    : %d", e.udpPort)
	log.Printf("  模式         : proxy (SOCKS5)")
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
	e.peersMu.Unlock()

	log.Println("")
	log.Printf("========== 节点就绪: %s ==========", clientID)
	log.Printf("  虚拟 IP   : %s", vip)
	if pubIP != "" && pubPort > 0 {
		log.Printf("  公网地址  : %s:%d", pubIP, pubPort)
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

	switch t {
	case "ready":
		payload, _ := msg["payload"].(map[string]interface{})

		e.virtualIP, _ = payload["virtualIp"].(string)
		if cidr, ok := payload["virtualNetwork"].(string); ok && cidr != "" {
			e.virtualCIDR = cidr
		}
		log.Printf("分配虚拟 IP: %s（网段 %s）", e.virtualIP, e.virtualCIDR)

		if serverIP, ok := payload["yourPublicIp"].(string); ok && serverIP != "" {
			e.mu.Lock()
			e.serverSeenIP = serverIP
			e.mu.Unlock()
			log.Printf("[信令] 服务端看到的本机出口 IP: %s（WS/TCP 出口，仅参考）", serverIP)
		}

		e.reportMetadata()

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

				if pid == "" || pip == "" {
					continue
				}

				e.maybeResetFailCountForPeer(pid, pubIP, pubPort)

				e.registerPeer(pid, pip, pubIP, pubPort, natType, assisted)
				if relayAddr != "" {
					e.peersMu.Lock()
					if pi, ok := e.peers[pid]; ok {
						pi.TurnRelayAddr = relayAddr
					}
					e.peersMu.Unlock()
				}
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

		if from != "" && pip != "" {
			e.peersMu.RLock()
			p, exists := e.peers[from]
			alreadyReady := exists && p.loggedReady
			e.peersMu.RUnlock()

			e.maybeResetFailCountForPeer(from, pubIP, pubPort)

			e.registerPeer(from, pip, pubIP, pubPort, natType, assisted)
			if relayAddr != "" {
				e.peersMu.Lock()
				if pi, ok := e.peers[from]; ok {
					pi.TurnRelayAddr = relayAddr
				}
				e.peersMu.Unlock()
			}

			if !alreadyReady {
				log.Printf("[信令] 节点上线: %s (vip=%s)", from, pip)
			}
			e.logPeerReady(from)
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
			e.relayMgr.MarkFallback(peerID)
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

	case "left":
		log.Printf("[信令] 节点离开: %s", from)

		if e.relayMgr != nil {
			e.relayMgr.ClearPeer(from)
		}

		natHoleActiveMu.Lock()
		delete(failCounts, from)
		delete(natHoleActive, from)
		natHoleActiveMu.Unlock()

		e.peersMu.Lock()
		delete(e.peers, from)
		e.peersMu.Unlock()

		e.cancelFallbackTimer(from)
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

	if e.relayMgr != nil {
		e.relayMgr.ClearPeer(peerID)
	}

	log.Printf("[NAT-HOLE] %s 出口变化 %s:%d → %s:%d，重置失败计数 (was=%d)",
		peerID, oldPubIP, oldPubPort, newPubIP, newPubPort, hadCount)
}

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

	if oldIP == "" || oldIP == newIP {
		return
	}

	e.mu.Lock()
	if e.natMeta != nil {
		e.natMeta.PublicEndpoint = newEndpoint
		e.natMeta.P2PEndpoint = newEndpoint
	}
	e.mu.Unlock()

	natHoleActiveMu.Lock()
	count := len(failCounts)
	if count > 0 {
		failCounts = make(map[string]int)
	}
	natHoleActiveMu.Unlock()

	log.Printf("[NAT-HOLE] 本机出口 IP 变化 %s → %s，重置所有失败计数 (count=%d)",
		oldIP, newIP, count)
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

func (e *Edge) registerPeer(peerID, virtualIP, pubIP string, pubPort int, natType string, assisted []string) {
	var udpAddr *net.UDPAddr
	if pubIP != "" && pubPort > 0 {
		udpAddr = &net.UDPAddr{IP: net.ParseIP(pubIP), Port: pubPort}
	}
	e.peersMu.Lock()
	_, existed := e.peers[peerID]
	if existed {
		p := e.peers[peerID]
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
			p.UDPAddr = udpAddr
		}
		if natType != "" {
			p.NATType = natType
		}
		if len(assisted) > 0 {
			p.AssistedEndpoints = assisted
		}
	} else {
		e.peers[peerID] = &PeerInfo{
			ClientID:          peerID,
			VirtualIP:         virtualIP,
			PubIP:             pubIP,
			PubPort:           pubPort,
			UDPAddr:           udpAddr,
			NATType:           natType,
			AssistedEndpoints: assisted,
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
		if instr.TargetVirtualIp != "" {
			p.VirtualIP = instr.TargetVirtualIp
		}
		if udpAddr != nil {
			p.UDPAddr = udpAddr
		}
	} else {
		e.peers[instr.TargetMac] = &PeerInfo{
			ClientID:  instr.TargetMac,
			VirtualIP: instr.TargetVirtualIp,
			UDPAddr:   udpAddr,
		}
	}
}

func (e *Edge) runNatHole(instr *NatHoleInstruction) {
	res := e.executeNatHole(instr)
	if res == nil {
		return
	}

	if res.State == PunchStateSucceeded {
		e.relayMgr.MarkP2P(instr.TargetMac)
	} else if res.State == PunchStateFailed {
		e.relayMgr.MarkFallback(instr.TargetMac)
	}

	p2pStatus := 0
	switch e.relayMgr.GetState(instr.TargetMac) {
	case ConnP2P:
		p2pStatus = 3
	case ConnTURN, ConnRelay:
		p2pStatus = 2
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

// ============ peer 探测处理 ============

func (e *Edge) notePeerProbe(addr *net.UDPAddr) {
	e.notePeerCommon(addr, false, false)
}

func (e *Edge) notePeerProbeTURN(addr *net.UDPAddr) {
	e.notePeerCommon(addr, false, true)
}

func (e *Edge) notePeerTraffic(addr *net.UDPAddr) {
	e.notePeerCommon(addr, true, false)
}

func (e *Edge) notePeerTrafficTURN(addr *net.UDPAddr) {
	e.notePeerCommon(addr, true, true)
}

func (e *Edge) notePeerCommon(addr *net.UDPAddr, isRealData bool, viaTURN bool) {
	now := time.Now().UnixMilli()

	e.peersMu.Lock()
	var best *PeerInfo
	bestScore := -1
	for _, p := range e.peers {
		if p.UDPAddr == nil || !p.UDPAddr.IP.Equal(addr.IP) {
			continue
		}
		score := 1
		if p.UDPAddr.Port == addr.Port {
			score = 2
		}
		if score > bestScore {
			best = p
			bestScore = score
		}
	}
	if best == nil {
		e.peersMu.Unlock()
		return
	}
	best.lastRecvAt = now
	if isRealData {
		best.hasRealData = true
	}
	best.UDPAddr = &net.UDPAddr{IP: addr.IP, Port: addr.Port}
	clientID := best.ClientID
	ip := addr.IP.String()

	needReply := false
	if !isRealData && !best.probeReplied {
		best.probeReplied = true
		needReply = true
	}
	e.peersMu.Unlock()

	if e.relayMgr == nil {
		return
	}

	if isRealData {
		state := e.relayMgr.GetState(clientID)
		if state != ConnP2P {
			log.Printf("[P2P] 从 %s (%s) 收到真实数据帧，升级为 P2P", clientID, ip)
			e.relayMgr.MarkP2P(clientID)
		}
		return
	}

	if !needReply {
		return
	}

	if e.relayMgr.GetState(clientID) != ConnP2P {
		log.Printf("[P2P] 从 %s (%s) 收到打洞探测，上报 P2P 并回 5 轮", clientID, ip)
		e.relayMgr.MarkP2P(clientID)
	}

	safeGo("probe-reply-5", func() {
		probe := buildPunchProbe(e.virtualIP)
		for i := 0; i < 5; i++ {
			select {
			case <-e.doneCh:
				return
			default:
			}
			e.writeProbe(addr, probe, viaTURN)
			if i < 4 {
				time.Sleep(100 * time.Millisecond)
			}
		}
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
	probe := buildPunchProbe(e.virtualIP)
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
	e.mu.Unlock()

	e.maybeResetOwnFailCounts(ep)

	log.Printf("[NAT] +assisted: %s (total=%d)", ep, count)
	e.reportMetadataThrottled()
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
