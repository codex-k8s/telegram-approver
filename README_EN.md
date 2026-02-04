<div align="center">
  <img src="docs/media/logo.png" alt="PAI logo" width="120" height="120" />
  <h1>telegram-approver</h1>
  <p>📨 Telegram approver for <code>yaml-mcp-server</code>: interactive approval of risky operations by one person in one chat.</p>
</div>

![Go Version](https://img.shields.io/github/go-mod/go-version/codex-k8s/telegram-approver)
[![Go Reference](https://pkg.go.dev/badge/github.com/codex-k8s/telegram-approver.svg)](https://pkg.go.dev/github.com/codex-k8s/telegram-approver)

🇷🇺 Русская версия: [README.md](README.md)

`telegram-approver` is a minimal HTTP service that receives approval requests from `yaml-mcp-server`, sends them to Telegram, and returns the decision. It supports:

- one active request at a time (others wait until timeout);
- **Approve / Deny / Deny with message** buttons;
- optional voice denial reason (STT via OpenAI);
- **long polling** and **webhook** modes;
- `healthz/readyz` endpoints.

---

## ✅ How it works

1. `yaml-mcp-server` calls `POST /approve` and keeps the connection open.
2. `telegram-approver` sends a Telegram message.
3. The user selects a decision or sends a denial reason.
4. `telegram-approver` returns JSON with `decision` and `reason`.

If the timeout expires, the response is `decision=error`, buttons are removed, and the message is updated with a timeout note.

---

## 🔗 Related repositories

- `yaml-mcp-server` — MCP gateway with YAML DSL and approver chains: https://github.com/codex-k8s/yaml-mcp-server
- `codexctl` — CLI orchestrator for environments and Codex workflows: https://github.com/codex-k8s/codexctl
- `project-example` — Kubernetes project example with ready manifests: https://github.com/codex-k8s/project-example

---

## 📦 Installation

Requirements: Go **>= 1.25.5**.

```bash
go install github.com/codex-k8s/telegram-approver/cmd/telegram-approver@latest
```

---

## 🔐 Telegram bot setup

1. Create a bot via **@BotFather** and get the token.
2. Obtain the user `chat_id`:
   - Send any message to the bot first.
   - Get `chat_id` via a helper bot/script or `getUpdates`.
   - Quick option: use **@userinfobot**.

> Important: the service accepts decisions **only from one chat**.

---

## ⚙️ Environment variables

All variables are prefixed with `TG_APPROVER_`:

- `TG_APPROVER_TOKEN` — Telegram bot token (**required**)
- `TG_APPROVER_CHAT_ID` — user chat ID (**required**)
- `TG_APPROVER_HTTP_ADDR` — HTTP listen address (default `:8080`)
- `TG_APPROVER_LANG` — messages language (`en`/`ru`, default `en`)
- `TG_APPROVER_APPROVAL_TIMEOUT` — max wait time (default `1h`)
- `TG_APPROVER_TIMEOUT_MESSAGE` — timeout text appended in Telegram (optional)
- `TG_APPROVER_WEBHOOK_URL` — webhook URL (optional)
- `TG_APPROVER_WEBHOOK_SECRET` — webhook secret (optional)
- `TG_APPROVER_OPENAI_API_KEY` — OpenAI API key for STT (optional)
- `TG_APPROVER_STT_MODEL` — STT model (default `gpt-4o-mini-transcribe`)
- `TG_APPROVER_STT_TIMEOUT` — STT timeout (default `30s`)
- `TG_APPROVER_LOG_LEVEL` — log level (`debug|info|warn|error`)
- `TG_APPROVER_SHUTDOWN_TIMEOUT` — graceful shutdown timeout (default `10s`)

Webhook mode is enabled **only if both** `TG_APPROVER_WEBHOOK_URL` and `TG_APPROVER_WEBHOOK_SECRET` are set.

---

## 📡 API

### `POST /approve`

**Request**:

```json
{
  "correlation_id": "req-123",
  "tool": "github_create_env_secret_k8s",
  "arguments": {
    "namespace": "ai-staging",
    "k8s_secret_name": "pg-password"
  },
  "timeout_sec": 3600
}
```

**Response**:

```json
{
  "decision": "approve",
  "reason": "approved"
}
```

Allowed decisions: `approve`, `deny`, `error`.

### `POST /webhook`

Telegram webhook endpoint. Secret is verified via `X-Telegram-Bot-Api-Secret-Token` header.

### `GET /healthz`, `GET /readyz`

Kubernetes health endpoints.

---

## 🧠 Telegram message format

- Markdown is used.
- Request parameters are shown as a JSON block.
- For `Deny with message`, the bot waits for text or voice and returns it as `reason`.

---

## 🗣 Voice reasons (STT)

If `TG_APPROVER_OPENAI_API_KEY` is set, the bot accepts voice messages and transcribes them via OpenAI `gpt-4o-mini-transcribe`. Audio is stored **in memory only** during transcription.

For voice transcription, `ffmpeg` is required (used to normalize the format for OpenAI):

```
sudo apt-get install -y ffmpeg
```

---

## 🧷 Security & limitations

- The service is **stateless** (no external DB).
- **One active request** at a time.
- Requests are assumed to contain no secrets (no redaction is applied).

---

## 🔧 Development & releases

```bash
./dev/update.sh
```

---

## 📄 License

See [LICENSE](LICENSE).
