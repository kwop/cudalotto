#!/bin/bash
# cudalotto monitor — checks miner status and alerts on block found or crash
# Runs via cron every 15 min during mining hours (23h-7h)

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="$SCRIPT_DIR/.env"

if [ ! -f "$ENV_FILE" ]; then
    echo "ERROR: $ENV_FILE not found. Copy .env.example to .env and configure it." >&2
    exit 1
fi

# shellcheck source=/dev/null
source "$ENV_FILE"

if [ -z "$ALERT_EMAIL" ] || [ -z "$LOGFILE" ]; then
    echo "ERROR: ALERT_EMAIL and LOGFILE must be set in $ENV_FILE" >&2
    exit 1
fi

SERVICE="btc-miner.service"
HOSTNAME=$(hostname)

mkdir -p "$(dirname "$LOGFILE")"

timestamp() { date '+%Y-%m-%d %H:%M:%S'; }

# Check if we're in mining hours (23-07)
hour=$(date +%H)
if [ "$hour" -ge 7 ] && [ "$hour" -lt 23 ]; then
    exit 0
fi

# Check service status
if ! systemctl is-active --quiet "$SERVICE"; then
    echo "$(timestamp) [ALERT] $SERVICE is not running!" >> "$LOGFILE"
    echo "$SERVICE crashed on $HOSTNAME at $(timestamp)" | \
        mail -s "[cudalotto] MINER DOWN on $HOSTNAME" "$ALERT_EMAIL"
    # Try to restart
    systemctl start "$SERVICE"
    echo "$(timestamp) [INFO] Attempted restart of $SERVICE" >> "$LOGFILE"
    exit 1
fi

# Get recent logs (last 15 minutes)
recent_logs=$(journalctl -u "$SERVICE" --since "15 min ago" --no-pager 2>/dev/null)

# Check for block found / accepted share
if echo "$recent_logs" | grep -qi "SHARE FOUND\|block found\|ACCEPTED"; then
    matches=$(echo "$recent_logs" | grep -i "SHARE FOUND\|block found\|ACCEPTED")
    echo "$(timestamp) [BLOCK?] Potential share/block found!" >> "$LOGFILE"
    echo "$matches" >> "$LOGFILE"
    {
        echo "POTENTIAL BLOCK/SHARE FOUND on $HOSTNAME!"
        echo ""
        echo "Time: $(timestamp)"
        echo ""
        echo "Matching log lines:"
        echo "$matches"
        echo ""
        echo "Full recent logs:"
        echo "$recent_logs" | tail -30
    } | mail -s "[cudalotto] SHARE/BLOCK FOUND on $HOSTNAME!" "$ALERT_EMAIL"
fi

# Extract and log hashrate
hashrate=$(echo "$recent_logs" | grep -oP '\d+\.\d+ [KMGT]?H/s' | tail -1)
if [ -n "$hashrate" ]; then
    echo "$(timestamp) [OK] Running — $hashrate" >> "$LOGFILE"
else
    echo "$(timestamp) [OK] Running — no hashrate data yet" >> "$LOGFILE"
fi

# Log rotation: keep last 1000 lines
if [ -f "$LOGFILE" ] && [ "$(wc -l < "$LOGFILE")" -gt 1000 ]; then
    tail -500 "$LOGFILE" > "${LOGFILE}.tmp" && mv "${LOGFILE}.tmp" "$LOGFILE"
fi
