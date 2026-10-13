# n2n-termux

在 **Android Termux** 里跑的点对点组网客户端。**无需 root，无需 TUN**，通过 **SOCKS5 代理**把应用层流量接入 n2n 虚拟网。

配合 [edge-signal](https://github.com/goyo123321/edge-signal) 使用，与 [n2n-go-client](https://github.com/goyo123321/n2n-go-client) 和 [n2n-android](https://github.com/goyo123321/n2n-android) 互通。

---

## ✨ 特性

- **无 root** — Termux 普通 App 权限即可运行
- **无 TUN** — 不碰 `/dev/net/tun`，用 SOCKS5 代理接入
- **LAN 直连** — 同 WiFi 场景自动发现 LAN 地址，绕过 NAT 直接内网通信
- **四级降级** — `LAN → P2P → TURN → WS`，连接永远可用
- **P2P 直连** — UDP 打洞 + 自定义可靠流协议（`N2NS` 帧），延迟 10-50ms
- **TURN 中继** — 手写 TURN-Lite 客户端，UDP 不通自动切 TCP
- **WS 中继兜底** — 通过 Cloudflare Worker 转发，不消耗额外资源
- **多目标打洞** — LAN + STUN 反射 + 观测 raddr 同时扫描，命中率大幅提升
- **Keepalive 自愈** — 15 秒多目标 PING，NAT 端口漂移后自动重连
- **DNS 内置修复** — 解决 Go 静态二进制读不到 Termux `/etc/resolv.conf` 的问题
- **多架构** — arm64 / armv7 / amd64 三种二进制
- **一键安装** — `curl | bash` 式脚本，全自动配置

---

## 🏗️ 架构

Termux 是普通 App，Android 内核**不允许**其创建 TUN 设备，所以不能像 PC / Android APK 那样接管系统路由。

替代方案：**用 SOCKS5 代理截获应用层 TCP 连接**，通过自定义帧协议在对端重建连接。

```
┌────────────────────────────────────────────────────────┐
│  Termux 里的应用（curl / ssh / wget / …）              │
│      │                                                  │
│      │ SOCKS5 请求 "连 10.64.0.3:8080"                 │
│      ▼                                                  │
│  ┌──────────────┐    ┌────────────────┐                │
│  │ SOCKS5 服务器 │───▶│ P2P 可靠流 /    │                │
│  │ socks5.go    │    │ WS 应用流       │                │
│  └──────────────┘    └────────┬───────┘                │
│                               │                         │
│                     四级数据通路：                       │
│                     ├─ LAN 直连（同 WiFi，<5ms）       │
│                     ├─ P2P UDP（10-50ms）              │
│                     ├─ TURN 中继（30-100ms）           │
│                     └─ WebSocket（100-300ms）           │
└───────────────────────────────┬────────────────────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │ edge-signal (Worker)  │
                    │ ├─ 信令路由            │
                    │ ├─ NAT 打洞协调        │
                    │ ├─ 下发 TURN 凭证      │
                    │ └─ WS 中继兜底         │
                    └───────────────────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │  对端客户端            │
                    │  n2n-termux /         │
                    │  n2n-go-client /      │
                    │  n2n-android          │
                    └───────────────────────┘
```

**数据面格式**：

| 通道 | 帧格式 | 传输 |
|------|--------|------|
| LAN / P2P / TURN | `N2NS` 自定义帧（SYN/DATA/ACK/FIN + 重传 + 窗口） | UDP |
| WS | JSON（`app_stream_open` / `app_stream_data` + base64） | WebSocket |

---

## 🚀 快速开始

### 前提

1. Android 手机装了 [Termux](https://f-droid.org/packages/com.termux/)（**强烈建议从 F-Droid 装**，Play Store 版本已停更）
2. 已部署 [edge-signal](https://github.com/goyo123321/edge-signal)，拿到 `wss://xxx.workers.dev` 地址

### 一键安装

```bash
curl -fsSL -o ~/install-n2n.sh https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh
bash ~/install-n2n.sh
```

回车后按提示输入：

```
Worker WSS 地址: wss://edge-signal.<你的子域>.workers.dev
连接密码 CONNECT_TOKEN:     ← 无则直接回车
房间名 ROOM_ID: testroom
客户端 ID CLIENT_ID: termux-a
显示名 NODE_NAME: Termux-A
SOCKS5 监听地址 (127.0.0.1:1080):     ← 直接回车
```
## install.sh 指定版本（推荐）

```bash
VERSION=v1.1.10 \
SIGNALING_URL="wss://edge-signal.<你的子域>.workers.dev" \
ROOM_ID="myroom" \
CLIENT_ID="termux-a" \
NODE_NAME="Termux-A" \
AUTO_START=1 \
bash ~/install-n2n.sh
```

## 非交互式部署（推荐）

```bash
SIGNALING_URL="wss://edge-signal.<你的子域>.workers.dev" \
ROOM_ID="testroom" \
CLIENT_ID="termux-a" \
NODE_NAME="Termux-A" \
CONNECT_TOKEN="" \
SOCKS5_LISTEN="127.0.0.1:1080" \
AUTO_START=1 \
bash ~/install-n2n.sh
```

要改的只有两处：

占位符 换成
edge-signal.<你的子域>.workers.dev 你部署 edge-signal 时 wrangler 输出的 URL
termux-a / Termux-A 每台设备不同（termux-a/termux-b）

---

两台设备的具体命令

设备 A

```bash
SIGNALING_URL="wss://edge-signal.<你的子域>.workers.dev" \
ROOM_ID="myroom" \
CLIENT_ID="termux-a" \
NODE_NAME="Termux-A" \
AUTO_START=1 \
bash ~/install-n2n.sh
```

设备 B

```bash
SIGNALING_URL="wss://edge-signal.<你的子域>.workers.dev" \
ROOM_ID="myroom" \
CLIENT_ID="termux-b" \
NODE_NAME="Termux-B" \
AUTO_START=1 \
bash ~/install-n2n.sh
```

同一房间 → ROOM_ID 相同
不同设备 → CLIENT_ID / NODE_NAME 不同

---

有 CONNECT_TOKEN 的服务端

服务端配了 npx wrangler secret put CONNECT_TOKEN，客户端就要带：

```bash
SIGNALING_URL="wss://edge-signal.<你的子域>.workers.dev" \
CONNECT_TOKEN="你设的token" \
ROOM_ID="myroom" \
CLIENT_ID="termux-a" \
NODE_NAME="Termux-A" \
AUTO_START=1 \
bash ~/install-n2n.sh
```
## 验证p2p直连
A开个窗口新建http监听
```bash
cd ~ && python3 -m http.server 8080
```

B开个新窗口做curl 测试

```bash
curl --socks5 127.0.0.1:1080 -v --max-time 15 http://10.64.0.2:8080/ -o /dev/null
```

没配就不写 CONNECT_TOKEN，脚本默认空。

---

更新 install.sh 到最新版

```bash
curl -fsSL -o ~/install-n2n.sh \
  https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh
```


装完自动启动。

### 后台运行

```bash
nohup ~/.n2n-termux/start.sh > ~/n2n.log 2>&1 &

# 看日志
tail -f ~/n2n.log
```

### 停止

```bash
~/.n2n-termux/stop.sh
```

---

## 📖 使用

### 应用通过 SOCKS5 访问虚拟 IP

**单次命令**（curl 支持 `--socks5`）：

```bash
curl --socks5 127.0.0.1:1080 http://10.64.0.3:8080/
```

**全局代理**（环境变量）：

```bash
export ALL_PROXY=socks5://127.0.0.1:1080
curl http://10.64.0.3:8080/
```

**SSH 通过 SOCKS5**：

```bash
ssh -o ProxyCommand='nc -X 5 -x 127.0.0.1:1080 %h %p' user@10.64.0.3
```

**其他 SOCKS5 客户端**：

| 应用 | 配置方式 |
|------|---------|
| `git` | `git config --global http.proxy socks5://127.0.0.1:1080` |
| `wget` | 不支持 SOCKS5，需 `proxychains` |
| `curl` | `--socks5 127.0.0.1:1080` |
| `scp` | `scp -o ProxyCommand='nc -X 5 -x 127.0.0.1:1080 %h %p'` |

### 从其他设备访问 Termux 上的服务

在 Termux 里起服务（例如 `python3 -m http.server 8080`），其他设备通过它们的虚拟 IP 直接访问：

```bash
# 从 PC 的 n2n-go-client
curl http://10.64.0.2:8080/
```

---

## ⚙️ 环境变量

| 变量 | 必填 | 默认值 | 说明 |
|------|:----:|--------|------|
| `SIGNALING_URL` | ✅ | — | edge-signal 的 WSS 地址 |
| `CONNECT_TOKEN` | ❌ | `""` | 连接密码（服务端配了才需要） |
| `ROOM_ID` | ❌ | `default-room` | 房间名，只允许 `[A-Za-z0-9_-]+` |
| `CLIENT_ID` | ❌ | 自动生成并持久化 | 客户端唯一 ID，多设备必须不同 |
| `NODE_NAME` | ❌ | hostname | 面板显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |
| `STUN_SERVERS` | ❌ | 国内 STUN | STUN 服务器列表（逗号分隔） |
| `UDP_PORT` | ❌ | `0` | P2P UDP 端口，`0`=自动分配 |

**虚拟网段**由服务端 `VIRTUAL_NETWORK` 决定，客户端从 `ready` 消息自动接收。默认 `10.64.0.0/24`。

---

## 📋 启动日志示例

正常启动（同 WiFi 场景）：

```
[DNS] 使用 /data/data/com.termux/files/usr/etc/resolv.conf 的 2 个 nameserver: [8.8.8.8:53 8.8.4.4:53]
n2n-termux v1.1.0 启动
[配置] 未设置 CONNECT_TOKEN
[P2P] UDP 监听端口 37221
[NAT] 启动预收集 LAN: [192.168.10.2:37221 192.168.10.11:37221]
[NAT] STUN stun.chat.bilibili.com:3478 → 120.239.134.121:47877
[NAT] STUN stun.hitv.com:3478 → 120.239.134.121:47878
[NAT] HardNAT（对称），ports_diff=1 pub=120.239.134.121:47877 endpoints=2
[WS] 连接 wss://url.jyece.kdns.fr/ws/testroom?cid=termux-a
已连接信令，Client ID: termux-a，节点名: Termux-A
分配虚拟 IP: 10.64.0.2（网段 10.64.0.0/24）
[信令] 服务端看到的本机出口 IP: 120.239.134.121（WS/TCP 出口，仅参考）
[NAT] 上报 LAN 地址: [192.168.10.2:37221 192.168.10.11:37221]
[信令] 上报 p2p_metadata: natType=HardNAT publicEndpoint="120.239.134.121:47877" multiExit=true assisted=2 lan=2
[SOCKS5] 监听 127.0.0.1:1080（用 curl --socks5 或 ALL_PROXY 访问虚拟 IP）
[TURN] endpoint: 111.171.194.230:3478 (scheme=turn transport=udp)
[TURN-Lite] ✅ Allocation 成功: relay=111.171.194.230:63458
[Keepalive] 启动，每 5s 刷新 3 个 STUN 服务器（仅在有 peer 时）
[P2P-Keepalive] 启动，每 15s 刷新 P2P 通道

================= 本机信息 =================
  Client ID   : termux-a
  节点名       : Termux-A
  虚拟 IP     : 10.64.0.2
  虚拟网段    : 10.64.0.0/24
  P2P 端口    : 37221
  模式         : proxy (SOCKS5)
==========================================
```

**对端上线 + LAN 直连打洞**：

```
[registerPeer] termux-b: 收到 assisted=1 个 [120.239.134.121:47742] lanEndpoints=1 个 [192.168.10.12:56629]
[registerPeer] termux-b: sortedLAN=[192.168.10.12:56629] (prefixes=2)
[信令] 节点上线: termux-b (vip=10.64.0.3)

========== 节点就绪: termux-b ==========
  虚拟 IP   : 10.64.0.3
  公网地址  : 120.239.134.121:47742
  LAN 地址  : [192.168.10.12:56629]
  STUN 端点 : [120.239.134.121:47742]
==========================================

[NAT-HOLE] 开始打洞 role=1 target=192.168.10.12:56629
[collectScanTargets] 去重后 2 个候选: [192.168.10.12:56629 120.239.134.121:47742]
[NAT-HOLE] 优先扫描 2 个候选端点
[P2P] 从 termux-b (192.168.10.12) 收到 Punch → P2PAvailable（等待数据帧验证）
[P2P] 收到 PONG → P2P 直连（双向通道已验证）
[连接] termux-b → P2P 直连（prev=p2p-avail，数据面已验证）
```

**P2P 数据面成功**（真 P2P）：

```
[SOCKS5] CONNECT 10.64.0.3:8080
[SOCKS5] CONNECT 10.64.0.3:8080 → P2P 直连 (stream=2)
```

**P2P 失败回落 WS**：

```
[SOCKS5] CONNECT 10.64.0.3:8080
[P2P-STREAM] 2 建流超时（2s），保留 conn 让调用方回落
[SOCKS5] P2P 建流失败(syn timeout)，回落 WS 中继
[SOCKS5] CONNECT 10.64.0.3:8080 → WS 中继 (stream=termux-a-1)
```

---

## 🔥 四级降级

```
① LAN 直连（同 WiFi 首选）
   ├─ 延迟 <5ms
   ├─ 零 NAT 穿越
   ├─ 无流量计费
   └─ 不同 LAN 则降级
       │
       ▼
② P2P UDP 直连
   ├─ 延迟 10-50ms
   ├─ 不消耗任何资源
   └─ 打洞失败则降级
       │
       ▼
③ TURN 中继
   ├─ 延迟 30-100ms
   ├─ 消耗 TURN 服务器带宽（Cloudflare 1000GB/月免费）
   └─ TURN 不可用则降级
       │
       ▼
④ WebSocket 中继
   ├─ 延迟 100-300ms
   ├─ 消耗 Worker 请求配额（每天 10 万）
   └─ 最后兜底
```

**每对 peer 独立决策**：A↔B 可以走 LAN，A↔C 走 P2P，A↔D 走 TURN。

---

## 🔍 P2P 数据流原理

### LAN 地址发现（双路径）

**路径 1：UDP connect 骗内核**

```go
conn, _ := net.Dial("udp4", "8.8.8.8:80")
localIP := conn.LocalAddr().(*net.UDPAddr).IP
```

原理：connect UDP **不真正发包**，只更新内核路由缓存。`getsockname()` 返回内核为该 socket 选定的出口 IP —— 即"出网卡"的 IP。

- WiFi 下返回 `192.168.x.x` —— 直接可用
- 蜂窝下返回运营商内网 IP（`10.x` / `100.64.x`）—— 会被过滤掉
- **不触发 netlink 权限检查，Termux 里也能用**

**路径 2：遍历 `net.Interfaces()`**

覆盖多网卡场景（WiFi + USB 网卡）。两条路径结果合并去重。

**上报链路**：`p2p_metadata` 里带 `lanEndpoints` → 服务端存储 → 广播 `joined` → 对端 `registerPeer` 时收到。

### 打洞候选（score 制匹配）

对端可能从**三个不同源地址**发来 Punch/ACK：

| 源 | 分数 | 场景 |
|:---|:---|:---|
| `p.UDPAddr` 完全匹配 | 4 | 公网地址未变 |
| `p.UDPAddr` IP 匹配 | 3 | 端口漂移 |
| `p.LanEndpoints` 完全匹配 | **5（最高）** | 同 WiFi 直连 |
| `p.LanEndpoints` IP 匹配 | 2 | LAN 端口漂移 |
| `p.AssistedEndpoints` 完全匹配 | 4 | STUN 反射 |
| `p.AssistedEndpoints` IP 匹配 | 1 | 反射端口漂移 |

**最高分匹配**优先。

### 发起方（A）

```
curl 通过 SOCKS5 发起 → "连 10.64.0.3:8080"
   ↓
socks5.go 查 peer 表 → 找到 termux-b
   ↓
relayMgr.IsP2P("termux-b") || IsP2PAvailable || IsTURN → true
   ↓
p2p_stream.go: OpenP2PStream()
   ↓
构造 N2NS SYN 帧（含 targetPort=8080）
   ↓
collectN2NSTargets 收集所有候选：
   ├─ LAN 地址（同 WiFi 优先）
   ├─ AssistedEndpoints（STUN 反射）
   ├─ lastRecvFrom（对端最近发来的源地址）
   ├─ peerAddr（注册分配）
   └─ peer.UDPAddr（观测 raddr）
   ↓
多目标发送（命中率大幅提升）
   ↓
等 SYNACK（超时 2s）
```

### 接收方（B）

```
udpReadLoop 收到 N2NS 帧
   ↓
findPeerIDByAddr → score 制匹配 termux-a
   ↓
acceptSYN（幂等：多目标重复 SYN 只建一个 stream）
   ↓
dial 127.0.0.1:8080
   ↓
回 N2NS SYNACK (Ack=1)
   ↓
启动数据泵：读 localConn → N2NS DATA → 发回 A
```

### 数据帧格式（21 字节头 + payload）

```
偏移  长度  字段
────────────────────────────
0     4     "N2NS"      魔数
4     1     ver         版本 1
5     1     type        SYN=1 SYNACK=2 DATA=3 ACK=4 FIN=5 RST=6 PING=7 PONG=8
6     4     streamID    大端 uint32（偶数）
10    4     seq         本帧序列号
14    4     ack         确认收到的对方 seq
18    1     flags       保留
19    2     payloadLen  大端 uint16
21    N     payload     数据
```

**可靠性机制**：

- 每帧带序列号 + ACK，接收方按序交付
- 未 ACK 200ms 重传，最多 8 次
- 滑动窗口 16 帧
- 乱序缓存后按序交付

### P2P 通道状态机

```
Unknown → P2PAvailable → P2P
           ↑                ↑
      收到 Punch/ACK    收到 PONG 或
      打洞握手成功      真实数据帧
```

**PING/PONG 语义**（关键）：

- 收到 **PING** → 只回 PONG，**不升级本端状态**（单向证明）
- 收到 **PONG** → 双向通道已验证 → **升级到 P2P**
- 收到 **SYN/SYNACK/DATA/ACK/FIN/RST** → **升级到 P2P**（真实数据帧）

**P2PAvailable 30 秒超时**：打洞握手成功但 30 秒内没升到 P2P → 强制降级到 TURN/WS，避免永远卡在中间态。

### P2P Keepalive（NAT 映射保持）

**问题**：CGNAT 的 UDP 端口映射老化时间通常 30-60 秒。P2P 建立后如果没流量，映射会消失，之后发包用新端口，对端识别不了。

**方案**：每 15 秒用 **N2NS PING 多目标**发送（走 `collectN2NSTargets` 同一套候选），命中率比单目标高得多。PING 也刷新对端 NAT 映射，双向保活。

**关键代码路径**：`p2p_stream.go` 的 `sendP2PFrame`：

```go
targets := e.collectN2NSTargets(s)
for _, addr := range targets {
    e.udpConn.WriteToUDP(buf, addr)   // 多目标发送
}
```

---

## 🐛 DNS 修复

Termux 环境有个坑：

- `/etc` 是 `/system/etc` 的符号链接，Android 只读，**里面没有 `resolv.conf`**
- Termux 真正的 DNS 配置在 `$PREFIX/etc/resolv.conf`
- **Go 静态二进制（`CGO_ENABLED=0`）不经过 libc**，`termux-exec` 拦不到它
- 结果：Go 硬编码读 `/etc/resolv.conf` 失败 → fallback 到 `[::1]:53` → `connection refused`

**客户端内置修复**：启动时主动读 `$PREFIX/etc/resolv.conf`，覆盖 `net.DefaultResolver`。

**验证**：启动日志第一行应该有：

```
[DNS] 使用 /data/data/com.termux/files/usr/etc/resolv.conf 的 2 个 nameserver: [8.8.8.8:53 8.8.4.4:53]
```

**自定义 DNS**：改 `$PREFIX/etc/resolv.conf` 即可。

---

## ❓ 常见问题

### Q: 装完启动报 `请设置 SIGNALING_URL`？

正常——`install.sh` 装的是二进制，不负责启动。用 `~/.n2n-termux/start.sh` 启动（它会 source `config.env`）。

### Q: SOCKS5 起来了吗？

```bash
ss -tlnp 2>/dev/null | grep 1080
# 期望：LISTEN 127.0.0.1:1080
```

或直接测协议握手：

```bash
printf '\x05\x01\x00' | nc 127.0.0.1 1080 | xxd
# 期望：00000000: 0500
```

### Q: curl 返回 `000` 立即失败？

**错误**：`找不到 VIP xxx 对应的 peer`——说明对端不在线或已离线。

**检查**：

```bash
grep "节点上线\|节点离开" ~/n2n.log | tail -5
```

### Q: 如何判断走的是 LAN / P2P / TURN / WS？

**看日志**：

```
[SOCKS5] CONNECT 10.64.0.3:8080 → P2P 直连 (stream=2)       ← LAN 或 P2P
[SOCKS5] CONNECT 10.64.0.3:8080 → TURN 中继 (...)           ← TURN
[SOCKS5] CONNECT 10.64.0.3:8080 → WS 中继 (stream=...)      ← WS
```

**看服务端面板**：

```
连接状态：p2p-B      ← P2P（含 LAN）
连接状态：TURN-B     ← TURN
连接状态：ws-B       ← WS
```

**中继流量**：如果 P2P/LAN 生效，**TURN 中继流量保持 0 B**。

### Q: 怎么知道走的是 LAN 不是公网 P2P？

**看打洞日志的源 IP**：

```
[P2P] 从 termux-b (192.168.10.12) 收到 Punch    ← LAN
[P2P] 从 termux-b (120.239.134.121) 收到 Punch  ← 公网 P2P
```

`192.168.x.x` = LAN 直连，`120.x.x.x` = 公网 P2P。

**面板 NAT 类型显示 `LAN`** 也说明有 LAN 地址（打洞首选 LAN）。

### Q: 面板 NAT 类型一直是"未知"？

**正常**。STUN 探测有时只拿到一个样本（比如 `natType=unknown`），不影响功能。

**有 LAN 地址时显示 `LAN`**，比 NAT 类型更直观——毕竟打洞首选就是 LAN。

### Q: curl 慢（3-5 秒）？

**原因**：每次请求都先尝试 P2P，2 秒超时后才回落 WS。

**排查**：

1. 看 A 端日志有没有 `[P2P-STREAM] 建流超时`
2. 看 B 端日志有没有 `[P2P-STREAM] 收到 SYN`

**如果 A 建流超时但 B 没收到 SYN** —— UDP 单向不通（CGNAT hairpin）。

**如果走 LAN 直连但依然慢** —— 检查是否真在同 WiFi，`[P2P] 从 ... 收到 Punch` 的源 IP 应该是 `192.168.x.x`。

### Q: 打洞一直失败？

**检查 UDP 出口 IP 是否相同**：

```
A: UDP 120.239.134.121:47877
B: UDP 120.230.243.39:5417     ← 应不同
```

**相同 = 同 CGNAT 出口**，P2P 大概率失败（需要 hairpin，运营商多半不支持）。

**换网络**：一台用 4G，一台用 WiFi，出口 IP 就会不同。

**如果两端在同一 WiFi**：应该走 LAN 直连（不受 CGNAT 影响）。

**即使 P2P 失败，TURN / WS 依然能通**——只是延迟高一些。

### Q: LAN 地址收集不到？

**检查启动日志**：

```
[NAT] 启动预收集 LAN: [...]   ← 有这行说明能收集
[NAT] 启动预收集 LAN 为空（可能蜂窝网络，或 WiFi 未就绪）  ← 说明收集失败
```

**常见原因**：

1. **纯蜂窝网络** —— 没有 LAN 接口，`net.Interfaces()` 返回空，正常现象
2. **手机在蜂窝下启动后切 WiFi** —— 后台每 500ms 重试一次，10 秒内自动补齐
3. **A 的默认路由不在 WiFi 上** —— Android 系统把蜂窝当主网卡，`detectOutboundIP` 返回运营商 IP 被过滤

**手工验证**（A 端 Termux 里跑）：

```bash
python3 -c "import socket; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.connect(('8.8.8.8',80)); print(s.getsockname()[0])"
```

- 输出 `192.168.x.x` → Go 也能拿到 LAN
- 输出 `10.x` / `100.64.x` → A 的默认路由不在 WiFi，需要在 Android 设置里把 WiFi 设为主网卡

### Q: TURN 一直失败/刷屏？

**原因**：服务端配的 TURN 服务器访问不通。

**修复**：

**方案 1（推荐）**：服务端 `edge-signal/wrangler.toml` 里注释掉 `TURN_SERVERS`：

```toml
# TURN_SERVERS = "turn://..."
```

**方案 2**：客户端连续失败 5 次后自动放弃（v1.0.2 起内置）。

### Q: Termux 切后台被杀？

**解决**：

```bash
termux-wake-lock
nohup ~/.n2n-termux/start.sh > ~/n2n.log 2>&1 &
```

**需要装 [Termux:API](https://f-droid.org/packages/com.termux.api/)**。

**长期方案**：Android 设置 → 电池 → 电池优化 → Termux → 不优化。

### Q: 如何让 Termux 开机自启？

装 [Termux:Boot](https://f-droid.org/packages/com.termux.boot/)：

```bash
mkdir -p ~/.termux/boot
cat > ~/.termux/boot/n2n.sh <<'EOF'
#!/data/data/com.termux/files/usr/bin/sh
termux-wake-lock
nohup $HOME/.n2n-termux/start.sh > $HOME/n2n.log 2>&1 &
EOF
chmod +x ~/.termux/boot/n2n.sh
```

### Q: 支持域名吗？

**不支持**。SOCKS5 请求带域名会返回 `Address type not supported`。

**原因**：n2n 虚拟网里没有 DNS 服务。

**变通**：直接用虚拟 IP，或在本地 hosts 里加映射。

### Q: 支持 IPv6 / UDP 吗？

**不支持**。SOCKS5 请求解析不支持 IPv6 目标；`UDP ASSOCIATE` 命令未实现（只支持 `CONNECT`）。

**原因**：底层数据面是可靠流（序列号 + ACK + 重传），对 UDP 应用不友好。绝大多数 Termux 场景（SSH/HTTP/DB）只需要 TCP。

### Q: 一次能跑几个实例？

同一台手机可以跑多个（不同 `SOCKS5_LISTEN` 端口、不同 `CLIENT_ID`、不同 `UDP_PORT`）。

**同机多实例测不出真 P2P** —— 两个实例出口 IP 相同，会走 hairpin 逻辑（大概率失败 → TURN/WS）。

**要验证 P2P 必须两台物理设备**，且出口 UDP IP 不同。

### Q: 面板显示 `p2p-avail` 不是 `p2p`？

**`p2p-avail`** = 打洞握手成功，但 PONG 或真实数据帧还没到，尚未确认双向通道。

**正常情况 1-2 秒内**会升级到 `p2p`：

```
[P2P] 从 termux-b 收到 Punch → P2PAvailable（等待数据帧验证）
[P2P] 收到 PONG → P2P 直连（双向通道已验证）       ← 这行出现就升 P2P
```

**如果一直停在 `p2p-avail`**：30 秒后会超时降级到 TURN/WS。

---

## 🔗 相关项目

| 项目 | 平台 | 模式 |
|------|------|------|
| [edge-signal](https://github.com/goyo123321/edge-signal) | Cloudflare Workers | 信令服务器 + 打洞协调 + WS 中继 |
| [n2n-go-client](https://github.com/goyo123321/n2n-go-client) | Linux / macOS / Windows | TUN 网卡（需 root/管理员） |
| [n2n-android](https://github.com/goyo123321/n2n-android) | Android APK | VpnService TUN |
| **n2n-termux** | Android Termux | **SOCKS5 代理（无 root）** |

三端**互通**：n2n-termux ↔ n2n-go-client ↔ n2n-android 可以互相访问。

---

## 🚨 限制

| 项 | 说明 |
|----|------|
| **应用透明性** | 需要显式配置 SOCKS5，不像 TUN 模式那样"自动生效" |
| **域名** | 不支持，只能用虚拟 IP |
| **IPv6** | 不支持 |
| **UDP 应用** | SOCKS5 只代理 TCP，`UDP ASSOCIATE` 未实现 |
| **性能** | 比 TUN 模式略慢（多一层 SOCKS5 和应用流封装） |
| **单设备 P2P** | 同机多实例测不出真 P2P（CGNAT 特性） |
| **CGNAT hairpin** | 运营商 CGNAT 通常不支持，同出口 IP 时 P2P 会失败。**同 WiFi 场景优先走 LAN** |

---

## 📄 License

MIT

---

## 🙏 致谢

- [pion/stun](https://github.com/pion/stun) — STUN 协议实现（NAT 探测）
- [gorilla/websocket](https://github.com/gorilla/websocket) — WebSocket 传输
- 服务端 [edge-signal](https://github.com/goyo123321/edge-signal) 提供信令、打洞协调、TURN 凭证
