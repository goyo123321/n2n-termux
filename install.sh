#!/usr/bin/env bash
#
# n2n-termux 一键部署 + 自动升级
#
# 用法（推荐）：
#   curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh -o ~/install_n2n_termux.sh
#   bash ~/install_n2n_termux.sh
#
# 安装路径：默认在当前工作目录下创建 .n2n-termux（可用 INSTALL_DIR 覆盖）
#
# 重新配置：
#   bash ~/install_n2n_termux.sh                  # 交互式询问
#   RECONFIG=1 bash ~/install_n2n_termux.sh       # 强制重新配置
#   KEEP_CONFIG=1 bash ~/install_n2n_termux.sh    # 强制使用现有配置
#   RESET=1 bash ~/install_n2n_termux.sh          # 完全重置
#

set -eu

REPO="goyo123321/n2n-termux"
BIN_NAME="n2n-termux"

# ★ 安装目录：默认当前工作目录下的 .n2n-termux
if [ -z "${INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$(pwd)/.n2n-termux"
fi
case "$INSTALL_DIR" in
    /*) ;;
    *)  INSTALL_DIR="$(pwd)/$INSTALL_DIR" ;;
esac

SIGNALING_URL="${SIGNALING_URL:-}"
CONNECT_TOKEN="${CONNECT_TOKEN:-}"
ROOM_ID="${ROOM_ID:-}"
CLIENT_ID="${CLIENT_ID:-}"
NODE_NAME="${NODE_NAME:-}"
SOCKS5_LISTEN="${SOCKS5_LISTEN:-}"
UDP_PORT="${UDP_PORT:-0}"
STUN_SERVERS="${STUN_SERVERS:-}"
VERSION="${VERSION:-}"
FORCE_UPDATE="${FORCE_UPDATE:-0}"
NO_LAUNCH="${NO_LAUNCH:-0}"
AUTO_START="${AUTO_START:-0}"
CHECK_ONLY="${CHECK_ONLY:-0}"
RECONFIG="${RECONFIG:-0}"
KEEP_CONFIG="${KEEP_CONFIG:-0}"
RESET="${RESET:-0}"

BIN_PATH="$INSTALL_DIR/$BIN_NAME"
VERSION_FILE="$INSTALL_DIR/.version"
CONFIG_FILE="$INSTALL_DIR/config.env"
LAUNCHER="$INSTALL_DIR/start.sh"
STOPPER="$INSTALL_DIR/stop.sh"
BACKGROUND="$INSTALL_DIR/background.sh"
PID_FILE="$INSTALL_DIR/n2n.pid"
LOG_FILE="$INSTALL_DIR/n2n.log"

GITHUB_API="https://api.github.com/repos/${REPO}"
GITHUB_DL="https://github.com/${REPO}/releases/download"
FALLBACK_VERSION="v1.2.1"

if [ ! -t 0 ] && [ -r /dev/tty ]; then
    exec < /dev/tty || true
fi

IS_TERMUX=0
IS_PROOT=0
ARCH=""
BIN_FILE=""

if [ -t 1 ] && [ "${TERM:-}" != "dumb" ]; then
    RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
    BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; BLUE=''; CYAN=''; NC=''
fi
log()  { printf "${BLUE}[*]${NC} %s\n" "$*"; }
ok()   { printf "${GREEN}[✓]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[!]${NC} %s\n" "$*"; }
err()  { printf "${RED}[✗]${NC} %s\n" "$*" >&2; }
info() { printf "${CYAN}  %s${NC}\n" "$*"; }

read_tty() {
    local var="$1"
    local prompt="${2:-}"
    local silent="${3:-0}"
    [ -n "$prompt" ] && printf "%s" "$prompt"
    local val=""
    if [ -r /dev/tty ]; then
        if [ "$silent" = "1" ]; then read -rs val < /dev/tty || true
        else read -r val < /dev/tty || true; fi
    else
        if [ "$silent" = "1" ]; then read -rs val || true
        else read -r val || true; fi
    fi
    [ "$silent" = "1" ] && [ -n "$prompt" ] && echo
    eval "$var=\"\$val\""
}

detect_env() {
    if [ -n "${PREFIX:-}" ] && [[ "$PREFIX" == *"com.termux"* ]]; then
        IS_TERMUX=1
    fi

    if [ -f "/.proot" ] || grep -q "proot" /proc/version 2>/dev/null; then
        IS_PROOT=1
    fi

    local uname_m="$(uname -m)"
    case "$uname_m" in
        aarch64|arm64)      ARCH="linux-arm64" ;;
        armv7l|armv8l|arm)  ARCH="linux-armv7" ;;
        x86_64|amd64)       ARCH="linux-amd64" ;;
        i686|i386)          ARCH="linux-386" ;;
        *) err "不支持的架构: $uname_m"; exit 1 ;;
    esac

    BIN_FILE="${BIN_NAME}-${ARCH}"

    if [ "$IS_TERMUX" = "1" ]; then
        log "环境: Termux"
    elif [ "$IS_PROOT" = "1" ]; then
        log "环境: proot Linux"
    else
        log "环境: Linux"
    fi
    log "架构: $uname_m → $ARCH"
    log "二进制名: $BIN_FILE"
}

ensure_ca_certs() {
    if [ "$IS_TERMUX" != "1" ]; then
        return 0
    fi

    local ca_paths=(
        "${PREFIX:-/data/data/com.termux/files/usr}/etc/tls/cert.pem"
        "${PREFIX:-/data/data/com.termux/files/usr}/etc/ca-certificates/ca-bundle.crt"
        "${PREFIX:-/data/data/com.termux/files/usr}/etc/ssl/certs/ca-certificates.crt"
    )

    for p in "${ca_paths[@]}"; do
        if [ -f "$p" ] && [ -s "$p" ]; then
            log "CA 证书已存在: $p"
            return 0
        fi
    done

    warn "未找到 CA 证书（WSS 连接会失败），正在安装 ca-certificates..."
    if command -v pkg >/dev/null 2>&1; then
        pkg install -y ca-certificates 2>&1 | tail -3 || true
    fi

    for p in "${ca_paths[@]}"; do
        if [ -f "$p" ] && [ -s "$p" ]; then
            ok "CA 证书安装成功: $p"
            return 0
        fi
    done

    warn "CA 证书安装失败，WSS 可能报 x509 错误"
    warn "手动修复: pkg install ca-certificates"
}

fix_termux_dns() {
    if [ "$IS_TERMUX" != "1" ]; then return 0; fi
    local resolv="${PREFIX:-/data/data/com.termux/files/usr}/etc/resolv.conf"

    if [ -f "$resolv" ] && grep -q "nameserver ::1" "$resolv" 2>/dev/null; then
        warn "Termux DNS 指向 [::1]，正在修复..."
        echo "nameserver 8.8.8.8" > "$resolv"
        echo "nameserver 1.1.1.1" >> "$resolv"
        ok "DNS 已修复: $resolv"
    elif [ ! -f "$resolv" ] || ! grep -q "nameserver" "$resolv" 2>/dev/null; then
        warn "Termux 缺少 DNS 配置，正在创建..."
        mkdir -p "$(dirname "$resolv")"
        echo "nameserver 8.8.8.8" > "$resolv"
        echo "nameserver 1.1.1.1" >> "$resolv"
        ok "DNS 配置已创建: $resolv"
    fi
}

migrate_legacy_binary() {
    local old_bin="$INSTALL_DIR/n2n-client"

    if [ -f "$old_bin" ]; then
        warn "检测到旧版二进制: $old_bin"
        warn "从 v1.2.1 起二进制名改为 $BIN_NAME，旧的将被清理"
        rm -f "$old_bin"
        ok "已清理旧版二进制"
    fi

    if command -v pgrep >/dev/null 2>&1; then
        local pids
        pids=$(pgrep -f "${INSTALL_DIR}/n2n-client" 2>/dev/null || true)
        if [ -n "$pids" ]; then
            warn "停止旧版残留进程 (PID: $pids)"
            # shellcheck disable=SC2086
            kill $pids 2>/dev/null || true
            sleep 1
            pids=$(pgrep -f "${INSTALL_DIR}/n2n-client" 2>/dev/null || true)
            if [ -n "$pids" ]; then
                # shellcheck disable=SC2086
                kill -9 $pids 2>/dev/null || true
            fi
        fi
    fi
}

read_installed_version() {
    [ -f "$VERSION_FILE" ] && cat "$VERSION_FILE" 2>/dev/null | tr -d '[:space:]' || echo ""
}

write_installed_version() {
    mkdir -p "$INSTALL_DIR"
    echo "$1" > "$VERSION_FILE"
}

resolve_latest_version() {
    if [ -n "$VERSION" ]; then echo "$VERSION"; return 0; fi
    local latest=""
    if command -v curl >/dev/null 2>&1; then
        latest=$(curl -fsSL --connect-timeout 8 --max-time 20 \
            "${GITHUB_API}/releases/latest" 2>/dev/null \
            | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
            | head -1 || true)
    elif command -v wget >/dev/null 2>&1; then
        latest=$(wget -qO- --timeout=20 "${GITHUB_API}/releases/latest" 2>/dev/null \
            | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
            | head -1 || true)
    fi
    if [ -n "$latest" ]; then echo "$latest"
    else warn "GitHub API 请求失败，回退到 $FALLBACK_VERSION" >&2; echo "$FALLBACK_VERSION"; fi
}

stop_running_client() {
    local stopped=0

    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null | tr -d '[:space:]')
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            log "停止运行中的客户端 (PID: $pid)"
            kill "$pid" 2>/dev/null || true
            sleep 1
            if kill -0 "$pid" 2>/dev/null; then
                warn "SIGTERM 未生效，使用 SIGKILL"
                kill -9 "$pid" 2>/dev/null || true
                sleep 0.5
            fi
            stopped=1
        fi
        rm -f "$PID_FILE"
    fi

    if command -v pgrep >/dev/null 2>&1; then
        local pids
        pids=$(pgrep -f "${INSTALL_DIR}/${BIN_NAME}" 2>/dev/null || true)
        if [ -n "$pids" ]; then
            log "发现残留进程 (PID: $pids)，停止..."
            # shellcheck disable=SC2086
            kill $pids 2>/dev/null || true
            sleep 1
            pids=$(pgrep -f "${INSTALL_DIR}/${BIN_NAME}" 2>/dev/null || true)
            if [ -n "$pids" ]; then
                # shellcheck disable=SC2086
                kill -9 $pids 2>/dev/null || true
            fi
            stopped=1
        fi
    fi

    [ "$stopped" = "1" ] && ok "旧进程已停止"
}

install_binary() {
    local version="$1"
    mkdir -p "$INSTALL_DIR"

    local url="${GITHUB_DL}/${version}/${BIN_FILE}"
    log "下载: $url"

    local tmp="${INSTALL_DIR}/.${BIN_NAME}.tmp.$$"
    trap 'rm -f "$tmp"' EXIT

    if command -v curl >/dev/null 2>&1; then
        if ! curl -fL --connect-timeout 10 --max-time 300 -o "$tmp" "$url" 2>&1; then
            err "下载失败: $url"
            err ""
            err "可能的原因："
            err "  1. 版本 ${version} 未发布对应 ${ARCH} 的二进制"
            err "  2. 版本过旧（v1.2.0 及之前的二进制名是 n2n-client-linux-*）"
            err ""
            err "请检查: https://github.com/${REPO}/releases"
            exit 1
        fi
    elif command -v wget >/dev/null 2>&1; then
        if ! wget -O "$tmp" "$url"; then
            err "下载失败: $url"
            exit 1
        fi
    else
        err "curl 和 wget 都不可用"
        info "安装: pkg install curl"
        exit 1
    fi

    chmod +x "$tmp"
    mv "$tmp" "$BIN_PATH"
    trap - EXIT

    ok "已安装: $BIN_PATH ($(du -h "$BIN_PATH" 2>/dev/null | cut -f1 || echo '?'))"
}

normalize_signaling_url() {
    local url="$SIGNALING_URL"
    url="$(echo "$url" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
    case "$url" in
        wss://*|ws://*) SIGNALING_URL="$url" ;;
        https://*)      SIGNALING_URL="wss://${url#https://}"; log "自动转换 https:// → wss://" ;;
        http://*)       SIGNALING_URL="ws://${url#http://}";   log "自动转换 http:// → ws://" ;;
        "")             SIGNALING_URL="" ;;
        *)              SIGNALING_URL="wss://${url}";          log "自动补全协议头 wss://" ;;
    esac
    if [ -n "$SIGNALING_URL" ]; then
        case "$SIGNALING_URL" in
            wss://?*|ws://?*) ok "SIGNALING_URL = $SIGNALING_URL" ;;
            *) err "SIGNALING_URL 格式非法: $SIGNALING_URL"; exit 1 ;;
        esac
    fi
}

# ============================================================
# 交互式配置（含重新配置选项）
# ============================================================
prompt_config() {
    if [ "$NO_LAUNCH" = "1" ]; then
        return 0
    fi

    if [ -f "$CONFIG_FILE" ] && [ -z "$SIGNALING_URL" ]; then
        # RESET
        if [ "$RESET" = "1" ]; then
            warn "RESET=1，完全重置配置"
            rm -f "$CONFIG_FILE"
            rm -f "$INSTALL_DIR/client_id"
            log "已删除 config.env 和 client_id"
        fi

        # KEEP_CONFIG
        if [ -f "$CONFIG_FILE" ] && [ "$KEEP_CONFIG" = "1" ]; then
            log "KEEP_CONFIG=1，强制使用已有配置"
            set -a
            # shellcheck disable=SC1090
            source "$CONFIG_FILE"
            set +a
            normalize_signaling_url
            return 0
        fi

        # RECONFIG or 交互询问
        if [ -f "$CONFIG_FILE" ] && [ "$RECONFIG" = "1" ]; then
            log "RECONFIG=1，强制重新配置"
        elif [ -f "$CONFIG_FILE" ]; then
            log "检测到已有配置: $CONFIG_FILE"
            echo ""
            info "当前配置摘要："
            grep -E '^(SIGNALING_URL|ROOM_ID|CLIENT_ID|NODE_NAME|SOCKS5_LISTEN)=' "$CONFIG_FILE" 2>/dev/null \
                | sed 's/^/    /' || true
            if grep -q '^CONNECT_TOKEN="[^"]\+"' "$CONFIG_FILE" 2>/dev/null; then
                echo '    CONNECT_TOKEN="***"（已设置）'
            fi
            echo ""

            local answer=""
            read_tty answer "是否重新配置？(Y=重新配置 / n=使用现有) [y/N] "
            case "$answer" in
                y|Y|yes|Yes|YES)
                    log "重新配置..."
                    ;;
                *)
                    log "使用现有配置"
                    set -a
                    # shellcheck disable=SC1090
                    source "$CONFIG_FILE"
                    set +a
                    normalize_signaling_url
                    return 0
                    ;;
            esac
        fi
    fi

    echo ""
    log "配置客户端（直接回车用默认值，输入 - 清空该字段）"

    if [ -z "$SIGNALING_URL" ]; then
        read_tty SIGNALING_URL "SIGNALING_URL (wss://xxx.workers.dev): "
        normalize_signaling_url
    fi
    if [ -z "$SIGNALING_URL" ]; then
        warn "未填写 SIGNALING_URL，跳过启动"
        NO_LAUNCH=1
        return 0
    fi

    if [ -z "$ROOM_ID" ]; then
        read_tty input "ROOM_ID [default-room]: "
        if [ "$input" = "-" ]; then ROOM_ID="default-room"
        else ROOM_ID="${input:-default-room}"; fi
    fi

    if [ -z "$CLIENT_ID" ]; then
        if [ -f "$INSTALL_DIR/client_id" ]; then
            local existing_cid
            existing_cid=$(tr -d '[:space:]' < "$INSTALL_DIR/client_id" 2>/dev/null || echo "")
            if [ -n "$existing_cid" ]; then
                log "复用已有 CLIENT_ID: $existing_cid"
                CLIENT_ID="$existing_cid"
            fi
        fi
        if [ -z "$CLIENT_ID" ]; then
            read_tty input "CLIENT_ID [自动生成]: "
            if [ "$input" = "-" ]; then CLIENT_ID=""
            else CLIENT_ID="$input"; fi
        fi
    fi

    if [ -z "$NODE_NAME" ]; then
        local hostname_default
        hostname_default="$(hostname 2>/dev/null || echo 'termux')"
        read_tty input "NODE_NAME [$hostname_default]: "
        if [ "$input" = "-" ]; then NODE_NAME="$hostname_default"
        else NODE_NAME="${input:-$hostname_default}"; fi
    fi

    if [ -z "$CONNECT_TOKEN" ]; then
        read_tty input "CONNECT_TOKEN [留空]: " 1
        if [ "$input" = "-" ]; then CONNECT_TOKEN=""
        else CONNECT_TOKEN="$input"; fi
    fi

    if [ -z "$SOCKS5_LISTEN" ]; then
        read_tty input "SOCKS5_LISTEN [127.0.0.1:1080]: "
        if [ "$input" = "-" ]; then SOCKS5_LISTEN="127.0.0.1:1080"
        else SOCKS5_LISTEN="${input:-127.0.0.1:1080}"; fi
    fi
}

save_config() {
    mkdir -p "$INSTALL_DIR"
    cat > "$CONFIG_FILE" <<EOF
# n2n-termux 配置
# 由 install.sh 生成于 $(date -Iseconds 2>/dev/null || date)

SIGNALING_URL="$(printf '%s' "${SIGNALING_URL:-}" | sed 's/"/\\"/g')"
CONNECT_TOKEN="$(printf '%s' "${CONNECT_TOKEN:-}" | sed 's/"/\\"/g')"
ROOM_ID="$(printf '%s' "${ROOM_ID:-default-room}" | sed 's/"/\\"/g')"
CLIENT_ID="$(printf '%s' "${CLIENT_ID:-}" | sed 's/"/\\"/g')"
NODE_NAME="$(printf '%s' "${NODE_NAME:-termux}" | sed 's/"/\\"/g')"
SOCKS5_LISTEN="$(printf '%s' "${SOCKS5_LISTEN:-127.0.0.1:1080}" | sed 's/"/\\"/g')"
UDP_PORT="${UDP_PORT:-0}"
STUN_SERVERS="$(printf '%s' "${STUN_SERVERS:-}" | sed 's/"/\\"/g')"
EOF
    chmod 600 "$CONFIG_FILE"
    ok "配置已保存: $CONFIG_FILE"
}

write_launcher() {
    cat > "$LAUNCHER" <<EOF
#!/usr/bin/env bash
set -eu

# 自动推导安装目录（start.sh 所在目录）
if [ -z "\${INSTALL_DIR:-}" ]; then
    INSTALL_DIR="\$(cd "\$(dirname "\$0")" && pwd)"
fi

CONFIG_FILE="\$INSTALL_DIR/config.env"
BIN="\$INSTALL_DIR/${BIN_NAME}"

if [ ! -f "\$CONFIG_FILE" ]; then echo "配置文件不存在: \$CONFIG_FILE"; exit 1; fi
if [ ! -x "\$BIN" ]; then echo "二进制不存在: \$BIN"; exit 1; fi

set -a
# shellcheck disable=SC1090
source "\$CONFIG_FILE"
set +a
exec "\$BIN" "\$@"
EOF
    chmod +x "$LAUNCHER"
    ok "前台启动脚本: $LAUNCHER"
}

write_background_launcher() {
    cat > "$BACKGROUND" <<EOF
#!/usr/bin/env bash
set -eu

if [ -z "\${INSTALL_DIR:-}" ]; then
    INSTALL_DIR="\$(cd "\$(dirname "\$0")" && pwd)"
fi

CONFIG_FILE="\$INSTALL_DIR/config.env"
BIN="\$INSTALL_DIR/${BIN_NAME}"
PID_FILE="\$INSTALL_DIR/n2n.pid"
LOG_FILE="\$INSTALL_DIR/n2n.log"

if [ ! -f "\$CONFIG_FILE" ]; then echo "配置文件不存在: \$CONFIG_FILE"; exit 1; fi
if [ ! -x "\$BIN" ]; then echo "二进制不存在: \$BIN"; exit 1; fi

if [ -f "\$PID_FILE" ]; then
    oldpid=\$(cat "\$PID_FILE" 2>/dev/null | tr -d '[:space:]')
    if [ -n "\$oldpid" ] && kill -0 "\$oldpid" 2>/dev/null; then
        echo "已在运行 (PID: \$oldpid)"
        exit 0
    fi
    rm -f "\$PID_FILE"
fi

set -a
# shellcheck disable=SC1090
source "\$CONFIG_FILE"
set +a

# 日志轮转：超过 5MB 时备份
if [ -f "\$LOG_FILE" ] && [ "\$(stat -c%s "\$LOG_FILE" 2>/dev/null || echo 0)" -gt 5242880 ]; then
    mv "\$LOG_FILE" "\$LOG_FILE.old"
fi

nohup "\$BIN" >> "\$LOG_FILE" 2>&1 &
NEW_PID=\$!
echo "\$NEW_PID" > "\$PID_FILE"
echo "已后台启动 (PID: \$NEW_PID)"
echo "日志: tail -f \$LOG_FILE"
EOF
    chmod +x "$BACKGROUND"
    ok "后台启动脚本: $BACKGROUND"
}

write_stopper() {
    cat > "$STOPPER" <<EOF
#!/usr/bin/env bash
set -eu

if [ -z "\${INSTALL_DIR:-}" ]; then
    INSTALL_DIR="\$(cd "\$(dirname "\$0")" && pwd)"
fi

PID_FILE="\$INSTALL_DIR/n2n.pid"
stopped=0

if [ -f "\$PID_FILE" ]; then
    pid=\$(cat "\$PID_FILE" 2>/dev/null | tr -d '[:space:]')
    if [ -n "\$pid" ] && kill -0 "\$pid" 2>/dev/null; then
        echo "停止中 (PID: \$pid)..."
        kill "\$pid" 2>/dev/null || true
        sleep 1
        if kill -0 "\$pid" 2>/dev/null; then
            kill -9 "\$pid" 2>/dev/null || true
        fi
        stopped=1
    fi
    rm -f "\$PID_FILE"
fi

if command -v pgrep >/dev/null 2>&1; then
    pids=\$(pgrep -f "\$INSTALL_DIR/${BIN_NAME}" 2>/dev/null || true)
    if [ -n "\$pids" ]; then
        kill \$pids 2>/dev/null || true
        sleep 1
        pids=\$(pgrep -f "\$INSTALL_DIR/${BIN_NAME}" 2>/dev/null || true)
        [ -n "\$pids" ] && kill -9 \$pids 2>/dev/null || true
        stopped=1
    fi
fi

if [ "\$stopped" = "1" ]; then
    echo "已停止"
else
    echo "未在运行"
fi
EOF
    chmod +x "$STOPPER"
    ok "停止脚本: $STOPPER"
}

launch_client() {
    if [ "$NO_LAUNCH" = "1" ]; then
        echo ""
        ok "安装完成（未启动）"
        info "前台启动: $LAUNCHER"
        info "后台启动: $BACKGROUND"
        info "停止:     $STOPPER"
        return 0
    fi

    if [ "$AUTO_START" != "1" ]; then
        local answer=""
        read_tty answer "是否立即启动客户端？[Y/n] "
        case "$answer" in
            n|N|no|No|NO)
                ok "安装完成（未启动）"
                info "前台启动: $LAUNCHER"
                info "后台启动: $BACKGROUND"
                return 0
                ;;
        esac
    fi

    if [ -z "$SIGNALING_URL" ]; then
        err "未设置 SIGNALING_URL，无法启动"
        return 1
    fi

    echo ""
    if [ "$AUTO_START" = "1" ]; then
        log "后台启动..."
        INSTALL_DIR="$INSTALL_DIR" "$BACKGROUND"
        sleep 2
        info "日志: tail -f $LOG_FILE"
    else
        ok "启动中（Ctrl+C 停止）..."
        exec "$LAUNCHER"
    fi
}

main() {
    echo ""
    echo "========================================================"
    echo "  n2n-termux 一键部署"
    echo "  https://github.com/${REPO}"
    echo "========================================================"
    echo ""
    log "安装目录: $INSTALL_DIR"
    echo ""

    if [ "$RESET" = "1" ] && [ -f "$CONFIG_FILE" ]; then
        warn "RESET=1：将删除 $CONFIG_FILE 和 client_id"
        local confirm=""
        read_tty confirm "确认重置？[y/N] "
        case "$confirm" in
            y|Y|yes|Yes|YES) ;;
            *) err "已取消"; exit 1 ;;
        esac
    fi

    detect_env
    ensure_ca_certs
    fix_termux_dns
    migrate_legacy_binary

    local installed latest need_update
    installed="$(read_installed_version)"

    log "查询最新版本..."
    latest="$(resolve_latest_version)"

    [ -n "$installed" ] && log "本地已装: $installed" || log "本地未装"
    log "远端最新: $latest"

    need_update=1
    if [ -n "$installed" ] && [ "$installed" = "$latest" ] && [ "$FORCE_UPDATE" != "1" ] \
        && [ -f "$BIN_PATH" ] && [ -x "$BIN_PATH" ]; then
        need_update=0
    fi

    if [ "$CHECK_ONLY" = "1" ]; then
        echo ""
        if [ "$need_update" = "1" ]; then
            ok "有新版本可更新: ${installed:-无} → $latest"
        else
            ok "已是最新版本: $latest"
        fi
        exit 0
    fi

    if [ "$need_update" = "0" ]; then
        ok "已是最新版本: $latest（跳过下载，FORCE_UPDATE=1 可强制重装）"
    else
        [ -n "$installed" ] && log "升级: $installed → $latest" || log "首次安装: $latest"
        stop_running_client
        install_binary "$latest"
        write_installed_version "$latest"
        ok "版本已更新: $latest"
    fi

    prompt_config
    [ -n "$SIGNALING_URL" ] && save_config

    if [ -n "${CLIENT_ID:-}" ]; then
        mkdir -p "$INSTALL_DIR"
        echo "$CLIENT_ID" > "$INSTALL_DIR/client_id"
        chmod 600 "$INSTALL_DIR/client_id"
    fi

    write_launcher
    write_background_launcher
    write_stopper

    echo ""
    ok "部署完成！"
    echo ""
    info "安装目录: $INSTALL_DIR"
    info "二进制:   $BIN_PATH"
    info "版本:     $latest"
    info "配置:     $CONFIG_FILE"
    info "日志:     $LOG_FILE"
    info "前台启动: $LAUNCHER"
    info "后台启动: $BACKGROUND"
    info "停止:     $STOPPER"
    echo ""

    launch_client
}

main "$@"
