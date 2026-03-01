# go-zork

A Z-Machine interpreter written in Go that runs classic Infocom text adventure games. Ships with three front-ends: a command-line player, a browser-based web UI, and an MCP server that lets AI agents play the game autonomously.

Scott Baker, https://github.com/scottmbaker/

Based on [MojoZork](https://github.com/icculus/mojozork) by Ryan C. Gordon.

> Only Z-Machine version 3 story files are supported. Zork 1 (`zork1.dat`) is included.

> AI Tools (Claude Code, Google Antigravity, Microsoft Copilot) have been used to perform some tasks in this project.

---

## Building

```bash
make             # builds _build/zork, _build/web, _build/mcp
make play        # play Zork 1 interactively in the terminal
make test        # runs the full Zork 1 walkthrough and verifies a score of 350/350
make walkthrough # runs the full Zork 1 walkthrough and prints the output
make clean       # removes all built files
```

Requires Go 1.21+.

### Docker

```bash
make docker-build                                         # builds image tagged smbaker/go-zork
docker run -it smbaker/go-zork                            # play interactively
docker run -p 8080:8080 smbaker/go-zork ./web             # web UI
docker run -p 8081:8081 smbaker/go-zork ./mcp --mode sse  # MCP SSE server
```

To push to Docker Hub:

```bash
make docker-push    # tags as docker.io/smbaker/go-zork:1.0.0 and pushes
```

---

## Programs

### `_build/zork` — CLI player

Play directly in the terminal.

```
Usage: zork [options] [story_file]

Options:
  -seed int      RNG seed (0 = current time)
  -save string   save file path (default "save.dat")
```

```bash
./_build/zork zork1.dat
./_build/zork -seed 42 zork1.dat
```

---

### `_build/web` — Browser UI

Serves two web-based UIs over HTTP. Each browser tab gets its own independent game session.

```
Usage: web [options]

Options:
  -story string   story file path (default "zork1.dat")
  -save  string   save file path  (default "save.dat")
  -port  string   listen address  (default ":8080")
```

```bash
./_build/web --story zork1.dat --port :8080
```

| Route | Description |
|---|---|
| `/terminal` | Full-screen xterm.js terminal (green-on-black, classic look) |
| `/chat` | Lightweight chat-style UI, no external dependencies |
| `/ws` | WebSocket endpoint (one ZMachine goroutine per connection) |
| `/` | Redirects to `/terminal` |

---

### `_build/mcp` — MCP server

Exposes Zork verbs as [Model Context Protocol](https://modelcontextprotocol.io/) tools so an AI agent (Claude, etc.) can play the game. Supports both stdio and SSE transports.

```
Usage: mcp [options]

Options:
  -story    string   story file path (default "zork1.dat")
  -save     string   save file path  (default "save.dat")
  -mode     string   transport: stdio or sse (default "stdio")
  -port     string   listen address for SSE mode (default ":8081")
  -log               print all game input/output to stdout
```

#### stdio mode (Claude Desktop, local agents)

```bash
./_build/mcp --story zork1.dat --mode stdio
```

Add to `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "zork": {
      "command": "/path/to/_build/mcp",
      "args": ["--story", "/path/to/zork1.dat", "--log"]
    }
  }
}
```

#### SSE mode (remote agents, HTTP clients)

```bash
./_build/mcp --story zork1.dat --mode sse --port :8081
```

#### MCP tools

| Tool | Parameters | Description |
|---|---|---|
| `get_intro` | — | Return the opening scene (call first) |
| `restart` | — | Reset the game to the beginning |
| `look` | — | Describe the current location |
| `inventory` | — | List carried items |
| `score` | — | Show score and move count |
| `wait` | — | Wait one turn |
| `pray` | — | Pray |
| `verbose` / `brief` / `superbrief` | — | Set description verbosity |
| `north` `south` `east` `west` | — | Move in cardinal directions |
| `northeast` `northwest` `southeast` `southwest` | — | Move diagonally |
| `up` / `down` | — | Move vertically |
| `examine` `take` `drop` `open` `close` | `object` | Interact with a single object |
| `read` `eat` `drink` `turn_on` `turn_off` | `object` | More single-object actions |
| `push` `pull` `climb` `move` | `object` | Physical manipulation |
| `light` `extinguish` `wave` `ring` `rub` | `object` | Special interactions |
| `cross` `echo` `enter` `exit` `smell` `listen` | `object` | Environment interactions |
| `wind` | `object` | Wind up a clockwork object |
| `put` | `item`, `container` | Put item in/on container |
| `attack` | `target`, `weapon` (opt.) | Attack a creature |
| `throw` | `item`, `target` | Throw item at target |
| `give` | `item`, `recipient` | Give item to character |
| `unlock` / `lock` | `object`, `key` | Lock or unlock with a key |
| `turn` | `object`, `tool` | Turn an object with a tool (e.g. bolt with wrench) |
| `tie` | `item`, `target` | Tie item to something |
| `insert` | `item`, `container` | Insert item into container |
| `say` | `text`, `to` (opt.) | Say something |
| `command` | `text` | Send any raw command (catch-all) |

The `--log` flag is useful for watching an AI play: all commands and responses are printed to stdout in real time.

---

## Helm Chart

A Helm chart is provided in `helm/go-zork/` for deploying the web or MCP server to Kubernetes.

### Install

```bash
# Web UI (default)
helm install zork helm/go-zork

# MCP SSE server
helm install zork helm/go-zork \
  --set mode=mcp

# Expose via NodePort
helm install zork helm/go-zork \
  --set service.type=NodePort \
  --set service.nodePort=31080

# Expose via Ingress
helm install zork helm/go-zork \
  --set ingress.enabled=true \
  --set ingress.host=zork.example.com
```

### Key values

| Value | Default | Description |
|---|---|---|
| `image.repository` | `smbaker/go-zork` | Container image repository |
| `image.tag` | `1.0.0` | Image tag |
| `mode` | `web` | Server to run: `web` or `mcp` |
| `web.port` | `8080` | Web server container port |
| `mcp.port` | `8081` | MCP server container port |
| `mcp.log` | `false` | Print game transcript to stdout |
| `service.type` | `ClusterIP` | Kubernetes service type |
| `service.port` | `80` | Service port |
| `service.nodePort` | `""` | NodePort number (30000–32767) when type is `NodePort` |
| `ingress.enabled` | `false` | Create an Ingress resource |
| `ingress.host` | `""` | Ingress hostname |
| `replicaCount` | `1` | Number of pod replicas |

---

## Architecture

```
pkg/zork/          Z-Machine interpreter (version 3)
cmd/zork/          CLI front-end
cmd/web/           HTTP + WebSocket server; terminal.html and chat.html embedded via go:embed
cmd/mcp/           MCP server (stdio + SSE via mark3labs/mcp-go)
```

The Z-Machine communicates entirely through Go channels:

```go
z.InputChan  <-chan string   // lines of user input sent to the interpreter
z.OutputChan chan<- []byte   // output chunks produced by the interpreter
done         <-chan error    // closed when the game ends
```

All three front-ends follow the same pattern: bridge their transport (stdin/stdout, WebSocket, or MCP tool call) to these two channels. The MCP server serialises concurrent tool calls through a mutex so each command/response pair is atomic.

---

## Testing

```bash
make test
```

Runs the full Zork 1 walkthrough (`walkthrough.txt`) with a fixed RNG seed and asserts a final score of 350/350 (maximum).
