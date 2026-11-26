# Porter & Odradek

Autonomous, local-first technical publisher for **LinkedIn** and **sidequestlab.dev**, controlled via **Telegram** and **CLI**.

- **Odradek** (`internal/odradek`): curates weekly themes, GitHub/Codeberg weekly commits, RSS/Hacker News, transcribes Telegram voice notes, renders high-DPI code/Mermaid PNGs locally via headless Chromium (`playwright-go`), and runs multimodal QA (Gemini Flash Free Tier).
- **Porter** (`internal/porter`): manages the Telegram Bot, live SQLite configuration (zero `.env`, zero restarts), adaptive topic trust, LinkedIn OAuth2 (with 60-day expiration alerts), and delivery to LinkedIn & `sidequestlab.dev`.

## Quick Start (Local Binary)

```bash
go build -o porter ./cmd/porter

# 1. Configure secrets & keys (stored in SQLite, no .env needed)
./porter config set telegram.bot_token "<TELEGRAM_BOT_TOKEN>"
./porter config set ai.deepseek_api_key "<DEEPSEEK_API_KEY>"
./porter config set ai.gemini_api_key "<GEMINI_API_KEY>"
./porter config set linkedin.client_id "<LINKEDIN_CLIENT_ID>"
./porter config set linkedin.client_secret "<LINKEDIN_CLIENT_SECRET>"

# 2. Start daemon and send /start in Telegram to discover your Admin Chat ID
./porter daemon
./porter config set telegram.admin_chat_id "<YOUR_CHAT_ID>"

# 3. Connect LinkedIn (via CLI or /auth in Telegram)
./porter auth linkedin
```

## Weekly Planning & GitHub/Codeberg Devlog

```bash
# Plan a theme for the week (LinkedIn + sidequestlab.dev)
./porter theme add --title "SQLite WAL Mode in Go" --angle "Single-writer concurrency" --sidequest=true

# Configure GitHub & Codeberg for weekly project updates
./porter config set curator.github_username "matheus"
./porter config set curator.codeberg_username "matheus"
```

## Telegram Commands

- `/config` — view live settings and toggle `dry_run`, `sidequest`, `tg_approval`
- `/set <key> <value>` — update any setting live without restarting
- `/week` / `/add_theme <title>` — view or queue weekly themes
- `/patterns` / `/trust <slug> [guidelines]` — manage Odradek trusted topic patterns
- `/run` — trigger the scheduled pipeline immediately
- `/devlog` — draft a weekly engineering update from GitHub & Codeberg
- `/auth` — connect or renew LinkedIn OAuth2 (60-day token)

## Docker Compose

```bash
docker compose up -d --build
```

## Arch Linux (`PKGBUILD` + `systemd`)

Builds, installs via `pacman`, creates the `porter` system user, enables/starts `porter.service` on first install, and restarts it automatically on every upgrade:

```bash
cd packaging/arch
makepkg -si
```

Configure live settings against the service database:
```bash
sudo -u porter porter --db /var/lib/porter/app.db config set telegram.bot_token "<TELEGRAM_BOT_TOKEN>"
```

## macOS (`launchd` Service Installer)

Compiles `porter` to `~/.local/bin/porter`, ensures Playwright Chromium is installed, and registers/restarts a `launchd` agent (`dev.sidequestlab.porter`):

```bash
make install-macos
```

## Cross-compiling for Linux (from macOS)

Because `porter` uses pure-Go SQLite (`CGO_ENABLED=0`), cross-compilation requires zero C toolchains:

```bash
make build-linux-amd64   # -> dist/porter-linux-amd64
make build-linux-arm64   # -> dist/porter-linux-arm64
```


