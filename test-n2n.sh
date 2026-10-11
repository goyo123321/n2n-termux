cat > ~/test-n2n.sh <<'SCRIPT'
#!/data/data/com.termux/files/usr/bin/bash
set -u

SIGNALING_URL="${SIGNALING_URL:-wss://url.jyece.kdns.fr}"
ROOM_ID="${ROOM_ID:-testroom}"
HTTP_PORT="${HTTP_PORT:-8080}"
SOCKS_A_PORT="${SOCKS_A_PORT:-1080}"
SOCKS_B_PORT="${SOCKS_B_PORT:-1081}"
BIN="${BIN:-$HOME/.n2n-termux/bin/n2n-client}"
STUN="${STUN:-stun.chat.bilibili.com:3478,stun.hitv.com:3478}"

LOG_DIR="$HOME/n2n-test-logs"
LOG_A="$LOG_DIR/a.log"; LOG_B="$LOG_DIR/b.log"; LOG_HTTP="$LOG_DIR/http.log"

G='\033[0;32m'; R='\033[0;31m'; Y='\033[1;33m'; B='\033[0;34m'; C='\033[0;36m'; N='\033[0m'
log(){ echo -e "${B}[*]${N} $1"; }; ok(){ echo -e "${G}[✓]${N} $1"; }
warn(){ echo -e "${Y}[!]${N} $1"; }; err(){ echo -e "${R}[✗]${N} $1" >&2; }
head(){ echo -e "${C}=== $1 ===${N}"; }

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
echo "=========================================="
echo "   n2n-termux 一键测试"
echo "=========================================="
echo "  HTTP:    $HTTP_PORT"
echo "  SOCKS-A: $SOCKS_A_PORT"
echo "  SOCKS-B: $SOCKS_B_PORT"
echo "=========================================="
echo ""

head "步骤 1/4: HTTP 服务"
cd "$HOME"
nohup python3 -m http.server "$HTTP_PORT" > "$LOG_HTTP" 2>&1 &
HTTP_PID=$!
sleep 1
kill -0 "$HTTP_PID" 2>/dev/null && ok "HTTP 运行中 (PID=$HTTP_PID)" || { err "HTTP 失败"; cat "$LOG_HTTP"; exit 1; }

head "步骤 2/4: 启动 A + B"

STUN_SERVERS="$STUN" SIGNALING_URL="$SIGNALING_URL" ROOM_ID="$ROOM_ID" \
CLIENT_ID="termux-a" NODE_NAME="Termux-A" SOCKS5_LISTEN="127.0.0.1:$SOCKS_A_PORT" UDP_PORT=0 \
nohup "$BIN" > "$LOG_A" 2>&1 &
PID_A=$!
log "A PID=$PID_A (SOCKS=$SOCKS_A_PORT)"

sleep 1

STUN_SERVERS="$STUN" SIGNALING_URL="$SIGNALING_URL" ROOM_ID="$ROOM_ID" \
CLIENT_ID="termux-b" NODE_NAME="Termux-B" SOCKS5_LISTEN="127.0.0.1:$SOCKS_B_PORT" UDP_PORT=0 \
nohup "$BIN" > "$LOG_B" 2>&1 &
PID_B=$!
log "B PID=$PID_B (SOCKS=$SOCKS_B_PORT)"

head "步骤 3/4: 等待虚拟 IP"

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
  err "超时"
  echo "--- A 尾 20 行 ---"; tail -20 "$LOG_A"
  echo "--- B 尾 20 行 ---"; tail -20 "$LOG_B"
  exit 1
fi
ok "A = $VIP_A"
ok "B = $VIP_B"

log "等 3s 让两端互相发现..."
sleep 3

grep -q "termux-b" "$LOG_A" && ok "A 已发现 B" || warn "A 未发现 B"
grep -q "termux-a" "$LOG_B" && ok "B 已发现 A" || warn "B 未发现 A"

head "步骤 4/4: 端到端测试"

echo ""
log "从 B 的 SOCKS5 访问 A:$HTTP_PORT"
echo "  命令: curl --socks5 127.0.0.1:$SOCKS_B_PORT http://$VIP_A:$HTTP_PORT/"
echo ""

HTTP_CODE=$(curl --socks5 "127.0.0.1:$SOCKS_B_PORT" --max-time 15 \
  -s -o /dev/null -w "%{http_code}" \
  "http://$VIP_A:$HTTP_PORT/" 2>/dev/null || echo "000")

if [ "$HTTP_CODE" = "200" ]; then
  ok "HTTP 200 ✅ 通！"
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
  if [ "$TTFB_MS" -lt 100 ]; then
    ok "→ P2P 直连"
  elif [ "$TTFB_MS" -lt 300 ]; then
    warn "→ TURN 中继"
  else
    warn "→ WS 中继"
  fi
fi

echo ""
head "DNS 修复验证"
grep "\[DNS\]" "$LOG_A" | head -1 || echo "  (A 端没打 [DNS] 日志)"
grep "\[DNS\]" "$LOG_B" | head -1 || echo "  (B 端没打 [DNS] 日志)"

echo ""
head "TURN 状态"
grep -E "TURN-Lite|TURN.*endpoint|TURN.*尝试|TURN.*失败|TURN.*成功|TURN.*放弃" "$LOG_A" | tail -10 || echo "  (A 端无 TURN 日志)"

echo ""
head "关键日志"

echo ""
echo "--- A 端 ---"
grep -E "P2P 直连|P2P-STREAM|收到 SYN|节点上线|WS 中继" "$LOG_A" | tail -10 || echo "  (无)"

echo ""
echo "--- B 端 ---"
grep -E "P2P 直连|P2P-STREAM|收到 SYN|节点上线|WS 中继|CONNECT" "$LOG_B" | tail -10 || echo "  (无)"

echo ""
head "路径判定"
if grep -q "→ P2P 直连" "$LOG_B"; then
  ok "P2P 直连 ✅"
elif grep -q "→ TURN 中继" "$LOG_B"; then
  warn "TURN 中继"
elif grep -q "→ WS 中继" "$LOG_B"; then
  warn "WS 中继"
else
  err "未识别"
fi

echo ""
echo "=========================================="
[ "$HTTP_CODE" = "200" ] && ok "🎉 测试通过" || err "❌ 测试失败"
echo "=========================================="
echo ""
echo "  日志: $LOG_DIR"
echo "    A: $LOG_A"
echo "    B: $LOG_B"
echo "    HTTP: $LOG_HTTP"
echo ""

echo -n "保留进程继续测？(y/N): "
read -r KEEP
if [[ "$KEEP" =~ ^[Yy]$ ]]; then
  trap - EXIT INT TERM
  ok "保留中"
  echo "  手动测: curl --socks5 127.0.0.1:$SOCKS_B_PORT http://$VIP_A:$HTTP_PORT/"
  echo "  停止:   pkill -f n2n-client; pkill -f 'http.server'"
fi
SCRIPT

chmod +x ~/test-n2n.sh
