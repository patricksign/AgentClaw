# Prerequisites — AgentClaw

Everything you need to install before running AgentClaw.

---

## 1. Go (required — runtime)

AgentClaw is a Go binary. You need Go 1.22 or later.

**macOS**
```bash
brew install go
```

**Linux**
```bash
# Download from https://go.dev/dl/ then:
tar -C /usr/local -xzf go1.24.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
```

**Verify**
```bash
go version
# go version go1.24.x ...
```

> `mattn/go-sqlite3` compiles a C library at `go build` time via CGO.
> You must have a C compiler available — see item 2.

---

## 2. C compiler / Xcode Command Line Tools (required — for SQLite)

The SQLite driver (`mattn/go-sqlite3`) uses CGO and requires a C compiler.

**macOS**
```bash
xcode-select --install
```

**Ubuntu / Debian**
```bash
sudo apt-get install -y build-essential gcc
```

**Fedora / RHEL**
```bash
sudo dnf install -y gcc make
```

**Verify**
```bash
gcc --version   # or: clang --version
```

---

## 3. SQLite3 CLI (optional — database inspection)

Not required to run the server, but useful for inspecting the database directly.

**macOS**
```bash
brew install sqlite
```

**Ubuntu / Debian**
```bash
sudo apt-get install -y sqlite3
```

**Verify**
```bash
sqlite3 --version
```

**Example usage**
```bash
sqlite3 agentclaw.db "SELECT id, title, status FROM tasks ORDER BY created_at DESC LIMIT 10;"
```

---

## 4. curl (optional — API testing)

Pre-installed on macOS and most Linux distributions.

```bash
curl --version
```

If missing:
```bash
# macOS
brew install curl

# Ubuntu / Debian
sudo apt-get install -y curl
```

---

## 5. jq (optional — JSON pretty-printing)

Makes API responses readable in the terminal.

**macOS**
```bash
brew install jq
```

**Ubuntu / Debian**
```bash
sudo apt-get install -y jq
```

**Verify**
```bash
jq --version
```

---

## 6. Node.js + wscat (optional — WebSocket testing)

Required only if you want to stream real-time events from the `/ws` endpoint.

**Install Node.js via nvm (recommended)**
```bash
curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash
source ~/.bashrc   # or: source ~/.zshrc
nvm install --lts
nvm use --lts
```

**Install wscat**
```bash
npm install -g wscat
```

**Verify**
```bash
node --version   # v18 or later
wscat --version
```

**Usage**
```bash
wscat -c ws://localhost:8080/ws
# Streams all agent events as JSON in real time
```

---

## 7. API Keys (required — LLM providers)

AgentClaw routes tasks to three LLM providers. Set the keys for providers you use.

| Provider | Env variable | Agents that use it | Get key |
|---|---|---|---|
| Anthropic | `ANTHROPIC_API_KEY` | idea, architect, breakdown, review | console.anthropic.com |
| MiniMax | `MINIMAX_API_KEY` | coding-agent-01, coding-agent-02 | platform.minimaxi.com |
| ZhipuAI (GLM) | `GLM_API_KEY` | test, docs, deploy, notify | open.bigmodel.cn |

**Set for current session**
```bash
export ANTHROPIC_API_KEY=sk-ant-...
export MINIMAX_API_KEY=...
export GLM_API_KEY=...
```

**Persist in shell profile**
```bash
echo 'export ANTHROPIC_API_KEY=sk-ant-...' >> ~/.zshrc   # or ~/.bashrc
echo 'export MINIMAX_API_KEY=...'          >> ~/.zshrc
echo 'export GLM_API_KEY=...'              >> ~/.zshrc
source ~/.zshrc
```

> **Minimum to get started:** set only `ANTHROPIC_API_KEY`.
> Tasks assigned to `idea`, `architect`, or `review` roles will work.
> Tasks for `coding`, `test`, `docs`, `deploy`, `notify` will fail with a clear
> error until the corresponding key is set.

---

## 8. Trello API credentials (optional — breakdown agent + idea board poller)

AgentClaw has two separate Trello integrations:

1. **Breakdown agent** — after decomposing a task, creates one Trello card per ticket in a target list.
2. **Idea board poller** — reads cards from a dedicated "Ideas" board and automatically triggers the idea → breakdown pipeline.

Both integrations are optional and degrade gracefully if credentials are missing.

### Step 1 — Get your API Key
1. Go to **https://trello.com/app-key**
2. Copy the **Key** shown at the top of the page

### Step 2 — Generate a Token
On the same page, click **"Token"** (or use this URL, replacing `YOUR_KEY`):
```
https://trello.com/1/authorize?expiration=never&scope=read,write&response_type=token&key=YOUR_KEY
```
Click **Allow** → copy the token string.

### Step 3 — Find your List ID

A **List** is a column on a Trello board (e.g. "Backlog", "To Do").

```bash
# Replace BOARD_ID, YOUR_KEY, YOUR_TOKEN
curl "https://api.trello.com/1/boards/BOARD_ID/lists?key=YOUR_KEY&token=YOUR_TOKEN" | jq '.[] | {id, name}'
```

To find your Board ID: open the board in a browser → the URL is
`https://trello.com/b/BOARD_ID/board-name` — copy the `BOARD_ID` part.

### Step 4 — Set env vars

```bash
export TRELLO_API_KEY=your-api-key
export TRELLO_TOKEN=your-token
export TRELLO_LIST_ID=your-list-id   # the column to push breakdown cards into
```

**Persist in shell profile**
```bash
echo 'export TRELLO_API_KEY=...'   >> ~/.zshrc
echo 'export TRELLO_TOKEN=...'     >> ~/.zshrc
echo 'export TRELLO_LIST_ID=...'   >> ~/.zshrc
source ~/.zshrc
```

> If any of the three Trello vars are missing, card creation is **silently
> skipped** — the breakdown agent still runs and returns its ticket JSON,
> just without pushing to Trello.

---

## 9. Trello idea board poller (optional — autonomous pipeline)

Set these vars to make AgentClaw **read ideas from Trello** and automatically
run the full idea → breakdown pipeline without any API calls.

| Env var | Purpose |
|---|---|
| `TRELLO_IDEA_BOARD_ID` | Board ID containing your idea cards (the "source") |
| `TRELLO_DONE_LIST_ID` | List to move processed cards into (prevents re-processing) |

`TRELLO_API_KEY` and `TRELLO_TOKEN` from section 8 are reused.

### How to find your Board ID

Open the board in a browser. The URL is:
```
https://trello.com/b/BOARD_ID/board-name
```
Copy the `BOARD_ID` segment (e.g. `xK7fBqP2`).

### How to find the Done List ID

```bash
curl "https://api.trello.com/1/boards/BOARD_ID/lists?key=YOUR_KEY&token=YOUR_TOKEN" | jq '.[] | {id, name}'
```

Pick the list you want processed cards moved to (e.g. "Processing", "In Progress").

### Set env vars

```bash
export TRELLO_IDEA_BOARD_ID=xK7fBqP2        # your idea board
export TRELLO_DONE_LIST_ID=your-list-id     # where to move processed cards
```

### How it works

Every **30 seconds** AgentClaw polls `TRELLO_IDEA_BOARD_ID` for open cards.
For each card that is **not** in `TRELLO_DONE_LIST_ID`:

1. An `idea` task is submitted to the queue → handled by `idea-agent-01` (Anthropic opus).
2. A `breakdown` task is submitted with `depends_on: [idea_task_id]` → handled by `breakdown-01` (Anthropic sonnet + Trello card creation).
3. The Trello card is immediately moved to `TRELLO_DONE_LIST_ID` so it is not re-processed.

> If `TRELLO_IDEA_BOARD_ID` is not set, the poller does not start.
> If `TRELLO_DONE_LIST_ID` is empty, cards are not moved (re-processing will occur on next poll).

---

## Quick install checklist

```bash
# macOS — install everything at once
xcode-select --install          # C compiler (for sqlite3 CGO)
brew install go sqlite jq curl  # runtime + CLI tools
npm install -g wscat            # WebSocket client (optional)

# LLM API keys
export ANTHROPIC_API_KEY=sk-ant-...
export MINIMAX_API_KEY=...
export GLM_API_KEY=...

# Trello (optional — breakdown agent + idea board poller)
export TRELLO_API_KEY=...
export TRELLO_TOKEN=...
export TRELLO_LIST_ID=...          # column to push breakdown cards into
export TRELLO_IDEA_BOARD_ID=...    # idea board to read from (poller)
export TRELLO_DONE_LIST_ID=...     # move processed idea cards here

# Build and run
cd agentclaw
make build
make run
```

```bash
# Ubuntu / Debian — install everything at once
sudo apt-get update
sudo apt-get install -y build-essential gcc golang sqlite3 jq curl
npm install -g wscat            # WebSocket client (optional)

# LLM API keys
export ANTHROPIC_API_KEY=sk-ant-...
export MINIMAX_API_KEY=...
export GLM_API_KEY=...

# Trello (optional — breakdown agent + idea board poller)
export TRELLO_API_KEY=...
export TRELLO_TOKEN=...
export TRELLO_LIST_ID=...          # column to push breakdown cards into
export TRELLO_IDEA_BOARD_ID=...    # idea board to read from (poller)
export TRELLO_DONE_LIST_ID=...     # move processed idea cards here

cd agentclaw
make build
make run
```

---

## Summary table

| Tool | Required? | Purpose |
|---|---|---|
| Go 1.22+ | **Yes** | Build and run the server |
| GCC / Clang | **Yes** | Compile `mattn/go-sqlite3` (CGO) |
| `ANTHROPIC_API_KEY` | **Yes** | LLM calls for idea/architect/breakdown/review agents |
| `MINIMAX_API_KEY` | For coding agents | LLM calls for coding-agent-01/02 |
| `GLM_API_KEY` | For test/docs/deploy/notify | LLM calls for GLM-based agents |
| `TRELLO_API_KEY` | For breakdown agent + idea poller | Trello credentials |
| `TRELLO_TOKEN` | For breakdown agent + idea poller | Trello credentials |
| `TRELLO_LIST_ID` | For breakdown agent | Target column for breakdown cards |
| `TRELLO_IDEA_BOARD_ID` | For idea board poller | Board to read ideas from |
| `TRELLO_DONE_LIST_ID` | For idea board poller | List to move processed idea cards to |
| sqlite3 CLI | No | Inspect database manually |
| curl | No | Test API endpoints from terminal |
| jq | No | Pretty-print JSON API responses |
| Node.js + wscat | No | Stream real-time WebSocket events |
