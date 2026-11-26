#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="${HOME}/.local/bin"
BIN_PATH="${BIN_DIR}/porter"
DATA_DIR="${HOME}/.local/share/porter"
MEDIA_DIR="${DATA_DIR}/media"
LOG_DIR="${HOME}/Library/Logs/porter"
PLIST_LABEL="dev.sidequestlab.porter"
PLIST_PATH="${HOME}/Library/LaunchAgents/${PLIST_LABEL}.plist"
UID_NUM="$(id -u)"

echo "==> Building porter binary..."
mkdir -p "${BIN_DIR}" "${DATA_DIR}" "${MEDIA_DIR}" "${LOG_DIR}" "${HOME}/Library/LaunchAgents"
cd "${REPO_ROOT}"
CGO_ENABLED=0 go build -ldflags="-s -w" -o "${BIN_PATH}" ./cmd/porter
chmod 755 "${BIN_PATH}"

echo "==> Ensuring Playwright Chromium is installed..."
go run github.com/mxschmitt/playwright-go/cmd/playwright install chromium || true

echo "==> Writing launchd service at ${PLIST_PATH}..."
cat > "${PLIST_PATH}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${PLIST_LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${BIN_PATH}</string>
    <string>daemon</string>
    <string>--db</string>
    <string>${DATA_DIR}/app.db</string>
    <string>--media-dir</string>
    <string>${MEDIA_DIR}</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key>
    <string>${HOME}</string>
    <key>PATH</key>
    <string>${HOME}/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>WorkingDirectory</key>
  <string>${DATA_DIR}</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${LOG_DIR}/porter.log</string>
  <key>StandardErrorPath</key>
  <string>${LOG_DIR}/porter.err.log</string>
</dict>
</plist>
EOF

echo "==> Loading / restarting launchd service..."
launchctl bootout "gui/${UID_NUM}/${PLIST_LABEL}" 2>/dev/null || true
launchctl bootstrap "gui/${UID_NUM}" "${PLIST_PATH}"
launchctl kickstart -k "gui/${UID_NUM}/${PLIST_LABEL}"

echo ""
echo "✅ Porter installed and running on macOS!"
echo "   Binary : ${BIN_PATH}"
echo "   DB     : ${DATA_DIR}/app.db"
echo "   Logs   : ${LOG_DIR}/porter.log"
echo ""
echo "Configure settings live (no restart needed):"
echo "   ${BIN_PATH} --db ${DATA_DIR}/app.db config set telegram.bot_token <TOKEN>"
