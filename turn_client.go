package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// ============ 数据结构 ============

type TURNServerInfo struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	TTL      uint32 `json:"ttl"`
}

type TURNResponse struct {
	Success bool             `json:"success"`
	Source  string           `json:"source,omitempty"`
	Servers []TURNServerInfo `json:"servers"`
	Error   string           `json:"error,omitempty"`
}

type TURNClient struct {
	mu           sync.RWMutex
	lite         *TURNLite
	tcpAlloc     *TURNTCPAllocation
	relayAddr    net.Addr
	server       *TURNServerInfo
	signalingURL string
	connectToken string
	edge         *Edge
	onMessage    func([]byte, net.Addr)
	stopCh       chan struct{}
	stopOnce     sync.Once
	alive        bool

	// 连续失败计数，避免无限重试刷屏
	consecutiveFails int
}

// ============ 构造 ============

func NewTURNClient(signalingURL string, connectToken string, edge *Edge) *TURNClient {
	return &TURNClient{
		signalingURL: signalingURL,
		connectToken: connectToken,
		edge:         edge,
		stopCh:       make(chan struct{}),
	}
}

func (tc *TURNClient) IsReady() bool {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if tc.lite != nil && tc.lite.IsAlive() {
		return true
	}
	if tc.tcpAlloc != nil && tc.tcpAlloc.IsAlive() {
		return true
	}
	return false
}

// ============ TURN URL 解析 ============

// turnEndpoint 描述一个解析好的 TURN 服务端地址。
type turnEndpoint struct {
	Addr      string // host:port
	Scheme    string // "turn" 或 "turns"
	Transport string // "udp" / "tcp" / "tls"
	HasCreds  bool   // URL 里是否内嵌了 user:pass@
}

// parseTURLEndpoint 解析 TURN URL。
//
// 输入示例（可能来自服务端 /api/turn-credentials 的 url 字段）：
//   turn:47.76.28.111:3478
//   turn:111.171.194.230:3478?transport=udp
//   turns:secure.example.com:5349
//   turn://user:pass@host:3478?transport=tcp
//   turn:1.2.3.4                （无端口，用默认）
func parseTURLEndpoint(raw string) turnEndpoint {
	s := strings.TrimSpace(raw)
	scheme := "turn"

	switch {
	case strings.HasPrefix(s, "turns://"):
		scheme = "turns"
		s = s[len("turns://"):]
	case strings.HasPrefix(s, "turn://"):
		scheme = "turn"
		s = s[len("turn://"):]
	case strings.HasPrefix(s, "turns:"):
		scheme = "turns"
		s = s[len("turns:"):]
	case strings.HasPrefix(s, "turn:"):
		scheme = "turn"
		s = s[len("turn:"):]
	}
	if strings.HasPrefix(s, "//") {
		s = s[2:]
	}

	// 剥 ?transport=xxx 后缀
	transport := ""
	if i := strings.Index(s, "?"); i >= 0 {
		query := s[i+1:]
		s = s[:i]
		for _, kv := range strings.Split(query, "&") {
			if strings.HasPrefix(kv, "transport=") {
				transport = strings.TrimPrefix(kv, "transport=")
			}
		}
	}

	// 剥 user:pass@ 前缀
	hasCreds := false
	if at := strings.LastIndex(s, "@"); at >= 0 {
		hasCreds = true
		s = s[at+1:]
	}

	// 无端口补默认
	if !strings.Contains(s, ":") {
		if scheme == "turns" {
			s += ":5349"
		} else {
			s += ":3478"
		}
	}

	// 未指定 transport 时按 scheme 推断
	if transport == "" {
		if scheme == "turns" {
			transport = "tcp"
		} else {
			transport = "udp"
		}
	}

	return turnEndpoint{
		Addr:      s,
		Scheme:    scheme,
		Transport: transport,
		HasCreds:  hasCreds,
	}
}

// ============ 请求凭证 + 建立分配 ============

func (tc *TURNClient) FetchAndSetup(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[TURN] FetchAndSetup panic: %v\n%s", r, debug.Stack())
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	if tc.IsReady() {
		return nil
	}

	httpBase := tc.signalingURL
	if strings.HasPrefix(httpBase, "wss://") {
		httpBase = "https://" + httpBase[len("wss://"):]
	} else if strings.HasPrefix(httpBase, "ws://") {
		httpBase = "http://" + httpBase[len("ws://"):]
	}
	httpBase = strings.TrimRight(httpBase, "/")

	credURL := fmt.Sprintf("%s/api/turn-credentials?ttl=86400", httpBase)
	if tc.connectToken != "" {
		credURL += "&token=" + url.QueryEscape(tc.connectToken)
	}

	log.Printf("[TURN] 请求凭证: %s", redactToken(credURL))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, credURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch credentials: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("turn-credentials 未授权 (http=%d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("credentials endpoint returned %d", resp.StatusCode)
	}

	var creds TURNResponse
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		return fmt.Errorf("decode credentials: %w", err)
	}
	if !creds.Success || len(creds.Servers) == 0 {
		return fmt.Errorf("no TURN servers: %s", creds.Error)
	}

	tc.mu.Lock()
	tc.server = &creds.Servers[0]
	tc.mu.Unlock()

	log.Printf("[TURN] 获取到 %s TURN: %s", creds.Source, creds.Servers[0].URL)
	return tc.setupAllocation(ctx)
}

// ============ 建立 TURN Allocation（TURN-Lite） ============

func (tc *TURNClient) setupAllocation(ctx context.Context) error {
	tc.mu.RLock()
	srv := tc.server
	tc.mu.RUnlock()
	if srv == nil {
		return fmt.Errorf("TURN server not set")
	}

	ep := parseTURLEndpoint(srv.URL)
	log.Printf("[TURN] endpoint: %s (scheme=%s transport=%s)",
		ep.Addr, ep.Scheme, ep.Transport)

	if srv.Username == "" && srv.Password == "" {
		log.Printf("[TURN] ⚠️ 服务端未提供 username/password（若 TURN 需要认证将失败）")
	}

	tryUDP := func() error {
		lite := NewTURNLiteWithTCP(ep.Addr, srv.Username, srv.Password, false)
		lite.onMessage = tc.wrapOnMessage()
		if err := lite.Allocate(); err != nil {
			return err
		}
		tc.mu.Lock()
		tc.lite = lite
		tc.relayAddr = lite.relayAddr
		tc.alive = true
		tc.mu.Unlock()
		log.Printf("[TURN] ✅ UDP transport 就绪: %s", tc.relayAddr)
		return nil
	}

	tryTCP := func() error {
		lite := NewTURNLiteWithTCP(ep.Addr, srv.Username, srv.Password, true)
		lite.onMessage = tc.wrapOnMessage()
		if err := lite.Allocate(); err != nil {
			return err
		}
		tc.mu.Lock()
		tc.lite = lite
		tc.relayAddr = lite.relayAddr
		tc.alive = true
		tc.mu.Unlock()
		log.Printf("[TURN] ✅ TURN over TCP 就绪: %s", tc.relayAddr)
		return nil
	}

	tryRFC6062 := func() error {
		tcpAlloc := NewTURNTCPAllocation(ep.Addr, srv.Username, srv.Password)
		tcpAlloc.onMessage = tc.wrapOnMessage()
		if err := tcpAlloc.Allocate(); err != nil {
			return err
		}
		tc.mu.Lock()
		tc.tcpAlloc = tcpAlloc
		tc.relayAddr = tcpAlloc.relayAddr
		tc.alive = true
		tc.mu.Unlock()
		log.Printf("[TURN] ✅ RFC 6062 TCP allocation 就绪: %s", tcpAlloc.GetRelayAddr())
		return nil
	}

	type attempt struct {
		name string
		run  func() error
	}
	var order []attempt

	switch ep.Transport {
	case "tcp", "tls":
		order = []attempt{
			{"TURN over TCP", tryTCP},
			{"RFC 6062 TCP allocation", tryRFC6062},
			{"UDP transport（回退）", tryUDP},
		}
	default:
		order = []attempt{
			{"UDP transport", tryUDP},
			{"TURN over TCP", tryTCP},
			{"RFC 6062 TCP allocation", tryRFC6062},
		}
	}

	var lastErr error
	for _, a := range order {
		log.Printf("[TURN] 尝试 %s", a.name)
		if err := a.run(); err != nil {
			lastErr = err
			log.Printf("[TURN] %s 失败: %v", a.name, err)
			continue
		}
		return nil
	}

	return fmt.Errorf("TURN 完全不可用: %w", lastErr)
}

func (tc *TURNClient) wrapOnMessage() func([]byte, net.Addr) {
	return func(data []byte, addr net.Addr) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[TURN] onMessage panic: %v\n%s", r, debug.Stack())
			}
		}()
		if tc.onMessage != nil {
			tc.onMessage(data, addr)
		}
	}
}

// ============ 后台重连 ============

func (tc *TURNClient) StartReconnectLoop() {
	safeGo("turn-reconnect", func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		const maxFails = 5

		for {
			select {
			case <-tc.stopCh:
				return
			case <-ticker.C:
				if tc.IsReady() {
					tc.mu.Lock()
					tc.consecutiveFails = 0
					tc.mu.Unlock()
					continue
				}

				tc.mu.RLock()
				fails := tc.consecutiveFails
				tc.mu.RUnlock()

				if fails >= maxFails {
					continue
				}

				log.Printf("[TURN] 检测到分配失效，尝试重建 (%d/%d)", fails+1, maxFails)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := tc.FetchAndSetup(ctx)
				cancel()

				tc.mu.Lock()
				if err != nil {
					tc.consecutiveFails++
					fc := tc.consecutiveFails
					tc.mu.Unlock()
					log.Printf("[TURN] 重建失败 (%d/%d): %v", fc, maxFails, err)
					if fc >= maxFails {
						log.Printf("[TURN] 连续失败 %d 次，放弃重试（WS 中继兜底）", maxFails)
					}
					continue
				}
				tc.consecutiveFails = 0
				tc.mu.Unlock()

				log.Printf("[TURN] ✅ 重建成功: %s", tc.GetRelayAddr())

				if tc.edge != nil && tc.edge.ws != nil {
					_ = tc.edge.ws.Send(map[string]interface{}{
						"type":      "turn_relay_info",
						"relayAddr": tc.GetRelayAddr(),
					})
				}

				if tc.edge != nil && tc.edge.relayMgr != nil {
					tc.edge.relayMgr.UpgradeRelaysToTURN()
				}
			}
		}
	})
}

// ============ 发送 ============

func (tc *TURNClient) Send(data []byte, remoteAddr net.Addr) error {
	tc.mu.RLock()
	lite := tc.lite
	tcpAlloc := tc.tcpAlloc
	tc.mu.RUnlock()

	udp, ok := remoteAddr.(*net.UDPAddr)
	if !ok {
		return fmt.Errorf("TURN 只支持 UDP 地址")
	}

	if lite != nil && lite.IsAlive() {
		return lite.SendTo(data, udp)
	}
	if tcpAlloc != nil && tcpAlloc.IsAlive() {
		return tcpAlloc.SendTo(udp, data)
	}
	return fmt.Errorf("TURN 未就绪")
}

// ============ 状态 ============

func (tc *TURNClient) GetRelayAddr() string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	if tc.relayAddr == nil {
		return ""
	}
	return tc.relayAddr.String()
}

// ============ 关闭 ============

func (tc *TURNClient) Close() {
	tc.stopOnce.Do(func() {
		close(tc.stopCh)
	})

	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.lite != nil {
		tc.lite.Close()
		tc.lite = nil
	}
	if tc.tcpAlloc != nil {
		tc.tcpAlloc.Close()
		tc.tcpAlloc = nil
	}
	tc.alive = false
}

// ============ 辅助 ============

func redactToken(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get("token") != "" {
		q.Set("token", "***")
		u.RawQuery = q.Encode()
	}
	return u.String()
}
