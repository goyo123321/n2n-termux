cat > ~/test-n2n.sh <<'SCRIPT'
#!/data/data/com.termux/files/usr/bin/bash
#
# n2n-termux 一键测试脚本
# 启动两个实例 + HTTP 服务，自动跑 SOCKS5 连接测试
#
# 用法：
#   bash ~/test-n2n.sh
#
# 环境变量（可选覆盖）：
#   SIGNALING_URL   信令服务器地址（必填）
#   ROOM_ID         房间名（默认 testroom）
#   HTTP_PORT       HTTP 服务端口（默认 8080）
#   SOCKS_A_PORT    A 的 SOCKS5 端口（默认 1080）
#   SOCKS_B_PORT    B 的 SOCKS5 端口（默认 1081）
#
set -u

# ============================================================
# 配置
# ============================================================
SIGNALING_URL="${SIGNALING_URL:-wss://url.jyece.kdns.fr}"
ROOM_ID="${ROOM_ID:-testroom}"
HTTP_PORT="${HTTP_PORT:-8080}"
SOCKS_A_PORT="${SOCKS_A_PORT:-1080}"
SOCKS_B_PORT="${SOCKS_B_PORT:-1081}"
BIN="${BIN:-$HOME/.n2n-termux/bin/n2n-client}"

LOG_DIR="$HOME/n2n-test-logs"
LOG_A="$LOG_DIR/a.log"
LOG_B="$LOG_DIR/b.log"
LOG_HTTP="$LOG_DIR/http.log"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

log()  { echo -e "${BLUE}[*]${NC} $1"; }
ok()   { echo -e "${GREEN}[✓]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[✗]${NC} $1" >&2; }
head() { echo -e "${CYAN}=== $1 ===${NC}"; }

# ============================================================
# 清理函数
# ============================================================
cleanup() {
    echo ""
    log "清理中..."

    # 杀 HTTP 服务（只杀自己启动的）
    if [ -n "${HTTP_PID:-}" ]; then
        kill "$HTTP_PID" 2>/dev/null || true
    fi

    # 杀 n2n 实例（只杀本次启动的 PID）
    if [ -n "${PID_A:-}" ]; then
        kill "$PID_A" 2>/dev/null || true
    fi
    if [ -n "${PID_B:-}" ]; then
        kill "$PID_B" 2>/dev/null || true
    fi

    sleep 0.5

    # 强制清理
    [ -n "${HTTP_PID:-}" ] && kill -9 "$HTTP_PID" 2>/dev/null || true
    [ -n "${PID_A:-}" ] && kill -9 "$PID_A" 2>/dev/null || true
    [ -n "${PID_B:-}" ] && kill -9 "$PID_B" 2>/dev/null || true

    ok "已清理"
}

trap cleanup EXIT INT TERM

# ============================================================
# 前置检查
# ============================================================
if [ ! -x "$BIN" ]; then
    err "找不到可执行文件: $BIN"
    echo "  请先运行 install.sh 部署"
    exit 1
fi

if [ -z "$SIGNALING_URL" ]; then
    err "SIGNALING_URL 未设置"
    echo "  用法: SIGNALING_URL=\"wss://xxx\" bash ~/test-n2n.sh"
    exit 1
fi

for cmd in curl python3 grep awk; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        err "缺少依赖: $cmd"
        echo "  安装: pkg install $cmd"
        exit 1
    fi
done

# ============================================================
# 检查端口占用
# ============================================================
check_port() {
    local port=$1
    if command -v ss >/dev/null 2>&1; then
        ss -tln 2>/dev/null | grep -q ":$port " && return 0
    fi
    # 备用：用 netstat
    netstat -tln 2>/dev/null | grep -q ":$port " && return 0
    return 1
}

for p in $HTTP_PORT $SOCKS_A_PORT $SOCKS_B_PORT; do
    if check_port "$p"; then
        warn "端口 $p 已被占用（继续尝试，可能会失败）"
    fi
done

# 清理残留进程
pkill -f "n2n-client.*testroom" 2>/dev/null || true
sleep 0.3

# ============================================================
# 准备
# ============================================================
mkdir -p "$LOG_DIR"
> "$LOG_A"
> "$LOG_B"
> "$LOG_HTTP"

echo ""
echo "=========================================="
echo "   n2n-termux 一键测试"
echo "=========================================="
echo "  信令:    $SIGNALING_URL"
echo "  房间:    $ROOM_ID"
echo "  HTTP:    $HTTP_PORT"
echo "  SOCKS-A: $SOCKS_A_PORT"
echo "  SOCKS-B: $SOCKS_B_PORT"
echo "=========================================="
echo ""

# ============================================================
# 1. 启动 HTTP 服务
# ============================================================
head "步骤 1/4: 启动 HTTP 服务"
cd "$HOME"
nohup python3 -m http.server "$HTTP_PORT" > "$LOG_HTTP" 2>&1 &
HTTP_PID=$!
log "HTTP PID=$HTTP_PID 端口=$HTTP_PORT"

sleep 1

if kill -0 "$HTTP_PID" 2>/dev/null; then
    ok "HTTP 服务运行中"
else
    err "HTTP 服务启动失败，看 $LOG_HTTP"
    cat "$LOG_HTTP"
    exit 1
fi

# ============================================================
# 2. 启动两个 n2n 实例
# ============================================================
head "步骤 2/4: 启动实例 A 和 B"

SIGNALING_URL="$SIGNALING_URL" \
ROOM_ID="$ROOM_ID" \
CLIENT_ID="termux-a" \
NODE_NAME="Termux-A" \
SOCKS5_LISTEN="127.0.0.1:$SOCKS_A_PORT" \
UDP_PORT=0 \
nohup "$BIN" > "$LOG_A" 2>&1 &
PID_A=$!
log "实例 A PID=$PID_A (SOCKS=$SOCKS_A_PORT)"

sleep 1

SIGNALING_URL="$SIGNALING_URL" \
ROOM_ID="$ROOM_ID" \
CLIENT_ID="termux-b" \
NODE_NAME="Termux-B" \
SOCKS5_LISTEN="127.0.0.1:$SOCKS_B_PORT" \
UDP_PORT=0 \
nohup "$BIN" > "$LOG_B" 2>&1 &
PID_B=$!
log "实例 B PID=$PID_B (SOCKS=$SOCKS_B_PORT)"

# ============================================================
# 3. 等待虚拟 IP 分配
# ============================================================
head "步骤 3/4: 等待虚拟 IP（最多 20 秒）"

VIP_A=""
VIP_B=""

for i in $(seq 1 20); do
    sleep 1
    if [ -z "$VIP_A" ]; then
        VIP_A=$(grep -m1 "分配虚拟 IP" "$LOG_A" 2>/dev/null | sed -n 's/.*IP: \([0-9.]*\).*/\1/p')
    fi
    if [ -z "$VIP_B" ]; then
        VIP_B=$(grep -m1 "分配虚拟 IP" "$LOG_B" 2>/dev/null | sed -n 's/.*IP: \([0-9.]*\).*/\1/p')
    fi
    printf "\r  等待中... %ds  A=%s B=%s" "$i" "${VIP_A:-?}" "${VIP_B:-?}"
    if [ -n "$VIP_A" ] && [ -n "$VIP_B" ]; then
        echo ""
        break
    fi
done
echo ""

if [ -z "$VIP_A" ] || [ -z "$VIP_B" ]; then
    err "等待超时"
    echo ""
    echo "--- A 日志尾部 ---"
    tail -20 "$LOG_A"
    echo ""
    echo "--- B 日志尾部 ---"
    tail -20 "$LOG_B"
    exit 1
fi

ok "实例 A 虚拟 IP: $VIP_A"
ok "实例 B 虚拟 IP: $VIP_B"

# 等对端互相看到
log "等待两端互相发现（3 秒）..."
sleep 3

if grep -q "节点上线.*termux-b" "$LOG_A"; then
    ok "A 已发现 B"
else
    warn "A 尚未发现 B"
fi

if grep -q "节点上线.*termux-a" "$LOG_B"; then
    ok "B 已发现 A"
else
    warn "B 尚未发现 A"
fi

# ============================================================
# 4. 测试连接
# ============================================================
head "步骤 4/4: 端到端测试"

echo ""
log "测试 1：从 B 的 SOCKS5 访问 A:$HTTP_PORT"
echo "  命令: curl --socks5 127.0.0.1:$SOCKS_B_PORT http://$VIP_A:$HTTP_PORT/"
echo ""

HTTP_CODE=$(curl --socks5 "127.0.0.1:$SOCKS_B_PORT" \
    --max-time 15 \
    -s -o /dev/null -w "%{http_code}" \
    "http://$VIP_A:$HTTP_PORT/" 2>/dev/null || echo "000")

if [ "$HTTP_CODE" = "200" ]; then
    ok "HTTP 状态码: $HTTP_CODE ✅ 通！"
else
    err "HTTP 状态码: $HTTP_CODE ❌"
fi

echo ""
log "测试 2：测量 TTFB（判断 P2P / TURN / WS）"
echo ""

TTFB=$(curl --socks5 "127.0.0.1:$SOCKS_B_PORT" \
    --max-time 15 \
    -s -o /dev/null -w "%{time_starttransfer}" \
    "http://$VIP_A:$HTTP_PORT/" 2>/dev/null || echo "0")

if [ -n "$TTFB" ] && [ "$TTFB" != "0" ]; then
    TTFB_MS=$(awk "BEGIN {printf \"%.0f\", $TTFB * 1000}")
    echo "  TTFB: ${TTFB}s (${TTFB_MS}ms)"

    if [ "$TTFB_MS" -lt 100 ]; then
        ok "延迟 < 100ms → P2P 直连"
    elif [ "$TTFB_MS" -lt 300 ]; then
        warn "延迟 100-300ms → TURN 中继"
    else
        warn "延迟 > 300ms → WS 中继（兜底）"
    fi
else
    err "TTFB 测量失败"
fi

echo ""
log "测试 3：从 A 的 SOCKS5 访问 B（反向）"
echo ""

# 需要在 B 上也跑一个服务，A 才能访问
# 简化：A 直接访问 B 的 SOCKS5（但 B 上没 HTTP 服务，会失败）
# 所以这里跳过反向测试
echo "  （跳过——B 上没有 HTTP 服务，反向测试需要额外启动）"

# ============================================================
# 5. 汇总关键日志
# ============================================================
echo ""
head "关键日志"

echo ""
echo "--- A 端 P2P 相关 ---"
grep -E "升级为 P2P|P2P 直连|P2P-STREAM|收到 SYN|节点上线|TURN 中继就绪|WS 中继" "$LOG_A" || echo "  (无)"

echo ""
echo "--- B 端 P2P 相关 ---"
grep -E "升级为 P2P|P2P 直连|P2P-STREAM|收到 SYN|节点上线|TURN 中继就绪|WS 中继|SOCKS5.*CONNECT" "$LOG_B" || echo "  (无)"

echo ""
echo "--- 连接路径判定 ---"
if grep -q "→ P2P 直连" "$LOG_B"; then
    ok "路径: P2P 直连 ✅"
elif grep -q "→ WS 中继" "$LOG_B"; then
    warn "路径: WS 中继（P2P 失败回落）"
elif grep -q "→ TURN 中继" "$LOG_B"; then
    warn "路径: TURN 中继"
else
    err "路径: 未识别（看日志）"
fi

# ============================================================
# 6. 结果总结
# ============================================================
echo ""
echo "=========================================="
if [ "$HTTP_CODE" = "200" ]; then
    ok "🎉 测试通过！"
else
    err "❌ 测试失败，HTTP 状态码=$HTTP_CODE"
fi
echo "=========================================="
echo ""
echo "  日志目录:  $LOG_DIR"
echo "    A:       $LOG_A"
echo "    B:       $LOG_B"
echo "    HTTP:    $LOG_HTTP"
echo ""
echo "  实时查看："
echo "    tail -f $LOG_A"
echo "    tail -f $LOG_B"
echo ""
echo "  清理所有进程："
echo "    pkill -f n2n-client"
echo "    pkill -f 'http.server $HTTP_PORT'"
echo ""

# ============================================================
# 交互：是否保留进程
# ============================================================
echo -n "保留进程继续测试？(y/N): "
read -r KEEP
if [[ "$KEEP" =~ ^[Yy]$ ]]; then
    echo ""
    ok "进程保留中（脚本退出后不清理）"
    echo ""
    echo "  手动测试："
    echo "    curl --socks5 127.0.0.1:$SOCKS_B_PORT http://$VIP_A:$HTTP_PORT/"
    echo ""
    echo "  停止："
    echo "    pkill -f n2n-client && pkill -f 'http.server $HTTP_PORT'"
    echo ""

    # 取消清理
    trap - EXIT INT TERM
fi
SCRIPT

chmod +x ~/test-n2n.sh
