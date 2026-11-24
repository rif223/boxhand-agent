#!/usr/bin/env bash
# Installs boxhand-agent as a systemd service.
#   sudo ./scripts/install.sh <path to binary> <user>
# The user runs the agent and needs the group tsbox (created by "sudo tsbox setup").
set -euo pipefail

[ "$(id -u)" = 0 ] || { echo "please run with sudo" >&2; exit 1; }
[ $# -eq 2 ] || { echo "usage: sudo $0 <path to boxhand-agent binary> <user>" >&2; exit 2; }
BIN="$1"
USER_NAME="$2"
HERE="$(cd "$(dirname "$0")/.." && pwd)"

id "$USER_NAME" >/dev/null 2>&1 || { echo "user $USER_NAME does not exist" >&2; exit 1; }
getent group tsbox >/dev/null || { echo "group tsbox missing: run 'sudo tsbox setup' first" >&2; exit 1; }
HOME_DIR="$(getent passwd "$USER_NAME" | cut -d: -f6)"

install -m 755 "$BIN" /usr/local/bin/boxhand-agent
usermod -aG tsbox "$USER_NAME"
sed -e "s#@USER@#$USER_NAME#g" -e "s#@HOME@#$HOME_DIR#g" "$HERE/deploy/boxhand-agent.service" \
	> /etc/systemd/system/boxhand-agent.service
systemctl daemon-reload
systemctl enable boxhand-agent
# restart, not start: a running agent would keep the old binary
systemctl restart boxhand-agent

echo
echo "Installed. Now, as $USER_NAME:"
echo "  boxhand-agent init --data $HOME_DIR/.config/boxhand-agent"
echo "and add the agent in Boxhand with the printed token and fingerprint."
