# n2n-termux

基于 **Termux** 的无 root P2P 组网客户端，通过 **SOCKS5 代理**实现应用层跨 NAT 直连，配合 [edge-signal](https://github.com/goyo123321/edge-signal) 使用。

**不需要 root，不需要 TUN，不需要 VpnService 权限**——直接在 Termux 里跑。

## ✨ 特性

- **零权限** — 普通 App 权限即可运行，不需要 root / TUN / VpnService
- **SOCKS5 代理** — 应用层截获 TCP 连接，透明转发到虚拟网
- **三级降级** — `P2P → TURN → WS`，连接永远可用
- **P2P 直连** — 自定义可靠流协议（N2NS），延迟低至 10-50ms
- **TURN 中继** — 内置 TURN-Lite，UDP 失败自动切 TCP
- **WS 兜底** — 最后走 Worker WebSocket 中继
- **断线重连** — WebSocket 无限重连（指数退避，封顶 5 分钟）
- **DNS 自修复** — 内置 resolver 补丁，Termux 里不用 proot 也能解析域名
- **轻量** — 单文件二进制，~5.3 MB
- **多架构** — arm64 / armv7 / amd64

## 🏗️ 架构

```
┌──────────────────────────────────────────────────┐
│              Termux 里的应用                       │
│    curl / ssh / wget / nc / 浏览器 …              │
└──────────────────────┬───────────────────────────┘
                       │ SOCKS5
                       ▼
┌──────────────────────────────────────────────────┐
│              n2n-termux 客户端                    │
│                                                    │
│   ┌──────────────┐     ┌────────────────┐        │
│   │ SOCKS5 服务器 │────▶│  P2P 可靠流 /  │        │
│   │              │     │  WS 应用流     │        │
│   └──────────────┘     └────────┬───────┘        │
│                                 │                 │
│                        ┌────────┴────────┐       │
│                        │  三级数据通道    │       │
│                        │  P2P / TURN / WS │       │
│                        └────────┬────────┘       │
└─────────────────────────────────┼────────────────┘
                                  │
                                  ▼
                     ┌────────────────────────┐
                     │  edge-signal (Worker)   │
                     │  信令 / 打洞 / TURN / WS │
                     └────────────────────────┘
                                  │
                                  ▼
                     ┌────────────────────────┐
                     │   对端客户端             │
                     │   (n2n-termux /         │
                     │    n2n-go-client /      │
                     │    n2n-android)         │
                     └────────────────────────┘
```

## 📡 三级降级

```
优先级 1: P2P 直连        ← 打洞成功，10-50ms，零消耗
   ↓ 打洞失败
优先级 2: TURN 中继       ← 30-100ms，消耗 TURN 服务器带宽
   ↓ TURN 不可用
优先级 3: WS 中继         ← 100-300ms，消耗 Worker 请求配额
```

**每一对 peer 独立决策**——A↔B 可能走 P2P，A↔C 走 TURN，互不影响。

## 🚀 安装

### 一键部署

```bash
curl -fsSL -o ~/install-n2n.sh \
  https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh
bash ~/install-n2n.sh
```

回车后逐项输入：

```
Worker WSS 地址: wss://your-worker.workers.dev
连接密码 CONNECT_TOKEN: ← 直接回车（无则留空）
房间名 ROOM_ID: myroom
客户端 ID CLIENT_ID: termux-a          ← 每台设备必须不同
显示名 NODE_NAME: Termux-A
SOCKS5 监听地址 (127.0.0.1:1080): ← 直接回车
```

**安装完成后**：

```bash
~/.n2n-termux/start.sh        # 前台启动
```

### 非交互部署

```bash
curl -fsSL -o ~/install-n2n.sh \
  https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh

SIGNALING_URL="wss://your-worker.workers.dev" \
ROOM_ID="myroom" \
CLIENT_ID="termux-a" \
NODE_NAME="Termux-A" \
AUTO_START=1 \
bash ~/install-n2n.sh
```

### 手动安装

```bash
mkdir -p ~/.n2n-termux/bin
cd ~/.n2n-termux/bin

# 根据架构选一个
curl -L -o n2n-client \
  https://github.com/goyo123321/n2n-termux/releases/latest/download/n2n-client-linux-arm64

chmod +x n2n-client
```

**架构对照**：

| `uname -m` | 下载文件 |
|------------|----------|
| `aarch64` / `arm64` | `n2n-client-linux-arm64` |
| `armv7l` / `armv7` | `n2n-client-linux-armv7` |
| `x86_64` / `amd64` | `n2n-client-linux-amd64` |

## ⚙️ 配置

配置保存在 `~/.n2n-termux/config.env`：

```bash
SIGNALING_URL="wss://your-worker.workers.dev"   # 必填
CONNECT_TOKEN=""                                 # 可选
ROOM_ID="myroom"                                 # 必填
CLIENT_ID="termux-a"                             # 必填，多设备必须唯一
NODE_NAME="Termux-A"                             # 显示名
SOCKS5_LISTEN="127.0.0.1:1080"                   # SOCKS5 监听地址
STUN_SERVERS="stun.chat.bilibili.com:3478,stun.hitv.com:3478"
```

### 环境变量说明

| 变量 | 必填 | 默认 | 说明 |
|------|------|------|------|
| `SIGNALING_URL` | ✅ | — | Worker WSS 地址 |
| `CONNECT_TOKEN` | ❌ | `""` | 连接密码（Worker 配了才需要）|
| `ROOM_ID` | ❌ | `default-room` | 房间名，`[A-Za-z0-9_-]+` |
| `CLIENT_ID` | ❌ | hostname | 客户端唯一 ID，多设备建议手填 |
| `NODE_NAME` | ❌ | hostname | 面板显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |
| `STUN_SERVERS` | ❌ | 内置 | STUN 服务器（逗号分隔）|
| `UDP_PORT` | ❌ | `0` | P2P UDP 端口，`0`=内核分配 |

## 📱 使用

### 启动

```bash
# 前台
~/.n2n-termux/start.sh

# 后台
nohup ~/.n2n-termux/start.sh > ~/n2n.log 2>&1 &

# 看日志
tail -f ~/n2n.log
```

**启动成功后日志**：

```
[DNS] 使用 /data/data/com.termux/files/usr/etc/resolv.conf 的 2 个 nameserver
n2n-termux dev 启动
[P2P] UDP 监听端口 45721
[NAT] STUN stun.chat.bilibili.com:3478 → 120.239.134.121:48105
[NAT] HardNAT（对称），ports_diff=1 pub=120.239.134.121:48105
已连接信令，Client ID: termux-a，节点名: Termux-A
分配虚拟 IP: 100.64.0.2（网段 100.64.0.0/16）
[SOCKS5] 监听 127.0.0.1:1080

================= 本机信息 =================
  Client ID   : termux-a
  节点名       : Termux-A
  虚拟 IP     : 100.64.0.2
  虚拟网段    : 100.64.0.0/16
  P2P 端口    : 45721
  模式         : proxy (SOCKS5)
==========================================
```

### 停止

```bash
~/.n2n-termux/stop.sh
```

或 `pkill -f n2n-client`。

### 用 SOCKS5 访问虚拟网

**方式 1：curl 显式指定**

```bash
curl --socks5 127.0.0.1:1080 http://100.64.0.3:8080/
```

**方式 2：全局代理**

```bash
export ALL_PROXY=socks5://127.0.0.1:1080
curl http://100.64.0.3:8080/
```

**方式 3：SSH 走 SOCKS5**

```bash
ssh -o ProxyCommand='nc -X 5 -x 127.0.0.1:1080 %h %p' user@100.64.0.3
```

**方式 4：其他 App 支持 SOCKS5 的**

把 `127.0.0.1:1080` 配置为代理即可。

### 验证

```bash
# 1. SOCKS5 是否在监听
ss -tln 2>/dev/null | grep 1080

# 2. 协议握手测试
printf '\x05\x01\x00' | nc 127.0.0.1 1080 | xxd
# 期望输出：00000000: 0500

# 3. 端到端测试（假设对端虚拟 IP 100.64.0.3）
curl --socks5 127.0.0.1:1080 -v --max-time 10 http://100.64.0.3:8080/
```

## 🧪 一键测试脚本

同机自测（两个实例 + HTTP + curl 全自动）：

```bash
cat > ~/test-n2n.sh <<'SCRIPT'
#!/data/data/com.termux/files/usr/bin/bash
set -u

SIGNALING_URL="${SIGNALING_URL:-wss://your-worker.workers.dev}"
ROOM_ID="${ROOM_ID:-testroom}"
HTTP_PORT="${HTTP_PORT:-8080}"
SOCKS_A_PORT="${SOCKS_A_PORT:-1080}"
SOCKS_B_PORT="${SOCKS_B_PORT:-1081}"
BIN="${BIN:-$HOME/.n2n-termux/bin/n2n-client}"
STUN="${STUN:-stun.chat.bilibili.com:3478,stun.hitv.com:3478}"

LOG_DIR="$HOME/n2n-test-logs"
LOG_A="$LOG_DIR/a.log"; LOG_B="$LOG_DIR/b.log"; LOG_HTTP="$LOG_DIR/http.log"

G='\033[0;32m'; R='\033[0;31m'; Y='\033[1;33m'; B='\033[0;34m'; N='\033[0m'
log(){ echo -e "${B}[*]${N} $1"; }; ok(){ echo -e "${G}[✓]${N} $1"; }
warn(){ echo -e "${Y}[!]${N} $1"; }; err(){ echo -e "${R}[✗]${N} $1" >&2; }

cleanup(){
    echo ""; log "清理中..."
    for p in "${HTTP_PID:-}" "${PID_A:-}" "${PID_B:-}"; do
        [ -n "$p" ] && kill "$p" 2>/dev/null
    done
    sleep 0.5
    for p in "${HTTP_PID:-}" "${PID_A:-}" "${PID_B:-}"; do
        [ -n "$p" ] && kill -9 "$p" 2>/dev/null
    done
    ok "已清理"
}
trap cleanup EXIT INT TERM

[ ! -x "$BIN" ] && { err "找不到: $BIN"; exit 1; }
mkdir -p "$LOG_DIR"; > "$LOG_A"; > "$LOG_B"; > "$LOG_HTTP"
pkill -f "n2n-client.*$ROOM_ID" 2>/dev/null || true
sleep 0.3

echo ""
log "启动 HTTP 服务 (端口 $HTTP_PORT)"
cd "$HOME"
nohup python3 -m http.server "$HTTP_PORT" > "$LOG_HTTP" 2>&1 &
HTTP_PID=$!
sleep 1

log "启动 termux-a (SOCKS5 $SOCKS_A_PORT)"
STUN_SERVERS="$STUN" SIGNALING_URL="$SIGNALING_URL" ROOM_ID="$ROOM_ID" \
CLIENT_ID="termux-a" NODE_NAME="Termux-A" SOCKS5_LISTEN="127.0.0.1:$SOCKS_A_PORT" UDP_PORT=0 \
nohup "$BIN" > "$LOG_A" 2>&1 &
PID_A=$!

sleep 1

log "启动 termux-b (SOCKS5 $SOCKS_B_PORT)"
STUN_SERVERS="$STUN" SIGNALING_URL="$SIGNALING_URL" ROOM_ID="$ROOM_ID" \
CLIENT_ID="termux-b" NODE_NAME="Termux-B" SOCKS5_LISTEN="127.0.0.1:$SOCKS_B_PORT" UDP_PORT=0 \
nohup "$BIN" > "$LOG_B" 2>&1 &
PID_B=$!

log "等待虚拟 IP 分配..."
VIP_A=""; VIP_B=""
for i in $(seq 1 20); do
    sleep 1
    [ -z "$VIP_A" ] && VIP_A=$(grep -m1 "分配虚拟 IP" "$LOG_A" 2>/dev/null | sed -n 's/.*IP: \([0-9.]*\).*/\1/p')
    [ -z "$VIP_B" ] && VIP_B=$(grep -m1 "分配虚拟 IP" "$LOG_B" 2>/dev/null | sed -n 's/.*IP: \([0-9.]*\).*/\1/p')
    printf "\r  等待中... %ds  A=%s B=%s" "$i" "${VIP_A:-?}" "${VIP_B:-?}"
    [ -n "$VIP_A" ] && [ -n "$VIP_B" ] && { echo ""; break; }
done
echo ""

if [ -z "$VIP_A" ] || [ -z "$VIP_B" ]; then
    err "超时"; tail -20 "$LOG_A"; echo "---"; tail -20 "$LOG_B"; exit 1
fi
ok "A = $VIP_A"
ok "B = $VIP_B"

log "等 3s 让两端互相发现..."
sleep 3

echo ""
log "从 B 的 SOCKS5 访问 A:$HTTP_PORT"
HTTP_CODE=$(curl --socks5 "127.0.0.1:$SOCKS_B_PORT" --max-time 15 \
  -s -o /dev/null -w "%{http_code}" \
  "http://$VIP_A:$HTTP_PORT/" 2>/dev/null || echo "000")

if [ "$HTTP_CODE" = "200" ]; then
    ok "HTTP 200 ✅"
else
    err "HTTP $HTTP_CODE ❌"
fi

echo ""
TTFB=$(curl --socks5 "127.0.0.1:$SOCKS_B_PORT" --max-time 15 \
  -s -o /dev/null -w "%{time_starttransfer}" \
  "http://$VIP_A:$HTTP_PORT/" 2>/dev/null || echo "0")

if [ -n "$TTFB" ] && [ "$TTFB" != "0" ]; then
    TTFB_MS=$(awk "BEGIN {printf \"%.0f\", $TTFB * 1000}")
    echo "  TTFB: ${TTFB}s (${TTFB_MS}ms)"
    if [ "$TTFB_MS" -lt 100 ]; then ok "→ P2P 直连"
    elif [ "$TTFB_MS" -lt 300 ]; then warn "→ TURN 中继"
    else warn "→ WS 中继"; fi
fi

echo ""
echo "=== 关键日志 ==="
echo "--- A ---"
grep -E "P2P 直连|P2P-STREAM|收到 SYN|TURN 中继就绪|WS 中继" "$LOG_A" | tail -5
echo "--- B ---"
grep -E "P2P 直连|P2P-STREAM|收到 SYN|TURN 中继就绪|WS 中继|CONNECT" "$LOG_B" | tail -5

echo ""
[ "$HTTP_CODE" = "200" ] && ok "🎉 测试通过" || err "❌ 测试失败"
echo ""
echo "  日志: $LOG_DIR"
echo ""

read -rp "保留进程继续测？(y/N): " KEEP
if [[ "$KEEP" =~ ^[Yy]$ ]]; then
    trap - EXIT INT TERM
    ok "保留中"
    echo "  手动测: curl --socks5 127.0.0.1:$SOCKS_B_PORT http://$VIP_A:$HTTP_PORT/"
fi
SCRIPT
chmod +x ~/test-n2n.sh
bash ~/test-n2n.sh
```

## 🔧 工作原理

### 为什么用 SOCKS5 而不是 TUN

| 平台 | 建 TUN | 数据面 |
|------|--------|--------|
| PC（有 root） | ✅ | 原始 IP 包 |
| Android APK（VpnService） | ✅ | 原始 IP 包 |
| **Termux（普通 App）** | ❌ 内核拒绝 | **SOCKS5 应用层** |

Android 内核只允许 `VpnService` 类应用创建 TUN。Termux 是普通 App，替代方案是用 SOCKS5 截获应用层的 TCP 连接。

### 三个数据通路

**P2P UDP 直连**：

```
SOCKS5 → P2P 建流 → N2NS SYN → P2P UDP → 对端 → dial 127.0.0.1:port
```

用自定义 **N2NS 帧格式**（魔数 + 序列号 + ack + 重传）在 UDP 上实现可靠流。

**TURN 中继**：

```
SOCKS5 → P2P 建流 → N2NS SYN → TURN 中继 → 对端
```

打洞失败时走 TURN，目标是**对端 relay 地址**。

**WebSocket 中继**：

```
SOCKS5 → app_stream_open JSON → Worker 转发 → 对端 → dial 127.0.0.1:port
```

走 Worker 信令通道，兜底方案。

### 打洞流程

```
1. 客户端启动 → STUN 探测 → 上报公网地址
2. 服务端下发 nat_hole_instruction
3. 客户端执行 N2NP probe + 端口扫描
4. 对端收到 probe → 回 5 轮 → 双方确认可通
5. relayMgr.states[peer] = "p2p"
```

### 降级时机

- 打洞失败（attempts 太多）→ 立即降级
- TURN 未就绪 → 降级到 WS
- P2P 建流超时 2 秒 → 回落 WS

## 🌐 DNS 修复（Termux 专用）

Termux 的 `/etc` 是指向 `/system/etc` 的软链（Android 只读），真正的 DNS 配置在 `$PREFIX/etc/resolv.conf`。Go 静态二进制不走 libc，读不到这个文件。

n2n-termux 内置 resolver 补丁，启动时**主动读 `$PREFIX/etc/resolv.conf`** 并覆盖 `net.DefaultResolver`。

**日志会显示**：

```
[DNS] 使用 /data/data/com.termux/files/usr/etc/resolv.conf 的 2 个 nameserver: [8.8.8.8:53 8.8.4.4:53]
```

看到这一行说明修复生效，**不需要 proot 包装**。

## 📋 启动日志解读

```
[DNS] 使用 ... nameserver: [...]                     ← DNS 修复生效
[P2P] UDP 监听端口 45721                              ← 本地 UDP socket 端口
[NAT] STUN ... → 120.239.134.121:48105              ← STUN 探测到公网地址
[NAT] HardNAT（对称），ports_diff=1 pub=...          ← NAT 类型
[WS] 连接 wss://.../ws/testroom?cid=termux-a         ← 信令连接
分配虚拟 IP: 100.64.0.2（网段 100.64.0.0/16）         ← 服务端分配
[SOCKS5] 监听 127.0.0.1:1080                         ← SOCKS5 起来
[TURN] TURN over TCP 就绪: 111.171.194.230:xxxxx     ← TURN 可用
[NAT-HOLE] ✅ 成功 role=1 target=...                 ← 打洞成功
[连接] termux-b → P2P 直连                            ← P2P 生效
```

## ❓ 常见问题

### Q: 应用无法访问虚拟 IP？

**检查 SOCKS5 是否配置**。n2n-termux **不会自动接管系统流量**——必须显式用 SOCKS5。

```bash
# 检查 SOCKS5 在监听
ss -tln | grep 1080

# 检查对端是否在线
grep "节点就绪" ~/n2n.log
```

### Q: DNS 解析失败？

看日志第一行有没有 `[DNS] 使用 ...`。没有的话：

```bash
cat $PREFIX/etc/resolv.conf
# 如果没有内容
cat > $PREFIX/etc/resolv.conf <<EOF
nameserver 8.8.8.8
nameserver 114.114.114.114
EOF
```

### Q: TURN 一直重试？

服务端的 TURN 服务器从你的网络不可达。**不影响功能**——会自动走 WS 中继兜底。

**TURN-Lite 特性**：UDP 失败 5 秒后自动切 TCP；连续失败 5 次后**放弃重试**，不再刷屏。

### Q: 面板显示 `p2p` 但 TTFB 还是 3 秒？

**单向 P2P** 场景——打洞探测双向成功但数据流建立失败。常见于同 CGNAT 出口的两端。

**修法**：`findPeerIDByAddr` 放宽到 IP 匹配（v1.0.4+ 已包含）。升级到最新版。

### Q: 每次请求慢 3 秒？

P2P 建流超时 2 秒 + WS 中继 1 秒。**只有第一次慢**——如果第 2 次起还是 3 秒，说明 P2P 每
