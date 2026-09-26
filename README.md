# cos-lite

A lightweight, Ubuntu-first MCP bridge with a terminal control plane.

`cos-lite` keeps the useful local-machine capabilities of Chat On Steroids without Electron, a browser extension, or ChatGPT conversation automation. The normal runtime is one small Go daemon. Running `cos` opens a dependency-free TUI for projects, tunnel setup, logs, browser tools, semantic code intelligence, skills, and daemon/autostart control.

v0.5 adds stateless multi-chat isolation, hot configuration reload, MCP Tasks, MCP Resources, hierarchical `AGENTS.md` instructions, and lightweight audit/OpenTelemetry tracing without adding a second daemon or a large dependency tree. v0.5.2 adds a live TUI Activity view with local-only bounded tool-output previews. **v0.6 is a performance-focused release**: Linux inotify replaces sub-second config polling, state is written only when it changes, Code Intel reuses language-server processes with idle eviction, audit/activity disk writes are batched asynchronously, browser contexts are garbage-collected after inactivity, and Skills/AGENTS metadata gets short-lived caches.

## What it looks like

```text
 cos-lite  Ubuntu MCP control plane
────────────────────────────────────────────────────────────────────
Daemon    ● running              pid 18422
Tunnel    ● connected            https://example.trycloudflare.com/...
Browser   enabled
Autostart enabled (boot)
CodeIntel enabled (3 server families found)
Skills    enabled (7 discovered)

Exposed projects
 ●  /trendseer             ~/Desktop/intern/trendseer_audit
 ●  /mdc                   ~/Desktop/Hackathons/MDC
 ○  /foundationdb          ~/Desktop/foundationdb

↑/↓ or j/k select   space exposure   a add   d remove   b browser   c code-intel
s start/stop   r restart   t tunnel   i skills   o activity   l logs   u autostart   q quit TUI
```

Quitting the TUI does **not** stop the daemon.

## MCP tools

### Core

| Tool | Purpose |
|---|---|
| `read` | Read files, line ranges, and folders inside exposed projects |
| `view_image` | Return PNG/JPEG/GIF/WebP as native MCP image content |
| `find` | Search project contents with ripgrep when available, otherwise a bounded built-in fallback |
| `apply_patch` | Exact-context Add/Update/Delete/Move patches |
| `exec_command` | Run shell commands, builds, tests, git, etc.; supports long-running sessions and PTYs |
| `write_stdin` | Poll/interact with running commands; `\u0003` sends SIGINT to the process group |
| `update_plan` | Keep a compact in-memory task plan |
| `skills` | Discover/load global or repo-local Skills with progressive disclosure |
| `code_intel` | LSP-backed definitions, references, hover, symbols, diagnostics, implementations, and rename previews |
| `exec` | Compose MCP tools with sandboxed JavaScript or a declarative call batch |

### Browser, no extension

Optional browser tools talk directly to a dedicated Chromium instance over CDP. Chromium starts lazily only when a browser tool is called.

- `browser_tabs`
- `browser_snapshot`
- `browser_screenshot`
- `browser_console`
- `browser_network`
- `browser_navigate`
- `browser_action`
- `browser_evaluate`

### External MCP servers

cos-lite uses **Codex's existing MCP configuration** as its only external MCP-server source of truth. Configure servers in:

```text
~/.codex/config.toml
```

using standard Codex `[mcp_servers.<name>]` tables. Enabled stdio servers are discovered automatically and their tools are exposed as `<server>.<tool>`. `CODEX_HOME` is respected when set. Changes to Codex's config are hot-reloaded by the daemon, so cos-lite keeps no separate external-MCP configuration file.

## Stateless multi-chat isolation

MCP `2026-07-28` is stateless. cos-lite combines explicit opaque capability handles with a request-scoped client/chat namespace:

```text
sess_<128-bit random>       terminal / PTY session
plan_<128-bit random>       in-memory work plan
browser_<128-bit random>    isolated Chromium context
task_<128-bit random>       MCP Task
```

The first call that creates one of these states returns its handle. The caller passes that handle on later calls. Handles are not enumerable, are generated with cryptographic randomness, and are intentionally omitted from audit/trace targets. This is the actual authorization boundary for transient state in stateless MCP.

For ChatGPT, an anonymized per-conversation value supplied in request metadata is hashed and used only to group status/audit events; the raw value is never persisted. It is **not** used as an authorization boundary. In normal use separate chats mint and retain different opaque handles, so their terminal sessions, plans, browser contexts, and Tasks stay separate unless a handle is deliberately copied between chats. The filesystem/project roots remain intentionally shared, so two chats can still edit the same repository at the same time and normal source-control/concurrency discipline still applies.

## MCP Tasks

cos-lite supports the `io.modelcontextprotocol/tasks` extension from MCP `2026-07-28`. If a client advertises that extension and an `exec_command` is still running after its initial yield, cos-lite returns a task handle instead of forcing the client to retain a live request.

Supported task methods:

```text
tasks/get
tasks/update
tasks/cancel
```

`taskId` is an opaque capability handle. Clients that do not advertise the Tasks extension keep the existing `session_id` + `write_stdin` behavior unchanged. Tasks survive hot configuration reloads, but they are in-memory state and do not survive a daemon restart.

## MCP Resources and project context

cos-lite exposes first-class Resources alongside tools:

```text
cos://status
cos://projects
cos://projects/<project>
cos://projects/<project>/git
cos://projects/<project>/instructions
cos://skills
cos://audit/recent
```

Hierarchical project instructions use the existing `AGENTS.md` convention. Root-level instructions are included in server instructions when present. For a nested file or directory, clients can resolve the full applicable hierarchy with:

```text
cos://projects/<project>/instructions?path=src/pkg/file.go
```

The resolver walks from the configured project root down to the requested path, applying each `AGENTS.md` in order. Root instructions are part of the server instructions; additional nested instructions are also surfaced automatically on path-sensitive `read`, `find`, `view_image`, `code_intel`, `exec_command`, and `apply_patch` calls. Instruction reads are bounded and symlinked `AGENTS.md` files are ignored.

## Skills

Skills are reusable instructions/workflows. They do **not** grant new permissions; they can only guide the model in using already-enabled MCP tools. cos-lite follows the cross-client Agent Skills directory convention instead of defining a private skill location.

Global Skills live at:

```text
~/.agents/skills/<id>/SKILL.md
```

Repo-local Skills live at:

```text
<project>/.agents/skills/<id>/SKILL.md
```

If you expose a broad parent such as `~/Desktop/Projects`, cos-lite also checks each immediate child directory for the same standard `.agents/skills` layout. That scan is bounded and non-recursive.

A minimal Skill:

```markdown
---
name: Review Pull Request
description: Review a code change for correctness, regressions, tests, and maintainability.
---

# Workflow

1. Read the relevant code and tests.
2. Inspect the diff.
3. Reproduce suspicious behavior where practical.
4. Report concrete findings with file/line evidence.
```

Skill metadata is advertised first. The full `SKILL.md` is loaded only when relevant. Supporting files are also supported, for example:

```text
review-pr/
├── SKILL.md
├── references/
│   └── checklist.md
└── scripts/
    └── inspect.sh
```

Global Skill management:

```bash
cos skill list
cos skill add ./my-skill
cos skill add ./my-skill review-pr
cos skill remove review-pr
cos skill enable
cos skill disable
```

`cos skill add` copies a bounded Skill package into the managed global library. Repo-local Skills stay in the repository and require no import step.

Model-facing Skill paths never expose the native config directory. They look like:

```text
~/.agents/skills/review-pr/SKILL.md
/foundationdb/.agents/skills/build/SKILL.md
```

## Semantic code intelligence

`code_intel` talks directly to installed language servers. Servers start lazily and are reused per project/language so repeated semantic operations avoid language-server startup. Idle servers are evicted after about 5 minutes, so warm calls stay fast without leaving an unbounded LSP farm resident.

For broad exposed roots, cos-lite selects the nearest language/project marker (`go.mod`, `Cargo.toml`, `tsconfig.json`, `pyproject.toml`, `CMakeLists.txt`, etc.) as the LSP root. File locations returned by an LSP outside approved project roots are redacted, and `workspace_symbols` filters external filesystem locations.

Supported language families:

| Language | Server |
|---|---|
| Go | `gopls` |
| C/C++ | `clangd` |
| Rust | `rust-analyzer` |
| Python | `basedpyright-langserver` or `pyright-langserver` |
| TypeScript/JavaScript | `typescript-language-server --stdio` |

Actions:

```text
definition
references
hover
document_symbols
workspace_symbols
implementations
diagnostics
rename
```

`rename` is deliberately a **preview**: it returns the LSP WorkspaceEdit but does not mutate files. Use `apply_patch` after reviewing the proposed change.

Examples of optional language-server installs:

```bash
go install golang.org/x/tools/gopls@latest
sudo apt install clangd
pipx install basedpyright
npm install -g typescript typescript-language-server
```

Check detected servers:

```bash
cos code-intel status
cos doctor
```

Toggle the MCP schema:

```bash
cos code-intel enable
cos code-intel disable
```

The TUI key is `c`.

## Intentionally excluded

This version does not implement ChatGPT conversation automation:

- no agents / worker chats
- no automatic Continue / compaction
- no ChatGPT DOM automation
- no session-finish coordination
- no model-picker automation
- no Chrome extension
- no Electron
- no Windows/macOS desktop-automation code

## Setup compared with Chat On Steroids

The ChatGPT-side idea is the same: a local MCP server is exposed through a tunnel and added as a ChatGPT MCP connector. cos-lite deliberately makes the machine-side setup smaller:

- one `cos` binary instead of Electron
- one MCP connector/endpoint for the enabled cos-lite tools
- no Chrome extension or pairing step
- projects are managed in the TUI and served automatically by the user service
- OpenAI Secure MCP Tunnel is a first-class TUI/CLI provider; Cloudflare/custom remain available
- after the connector is added to ChatGPT once, normal reboots do not require setup again

With OpenAI Secure MCP Tunnel, `cos endpoint` prints the `tunnel_id` to select/paste in ChatGPT. With URL-based tunnel providers it prints the public MCP URL.

## Requirements

The prebuilt binary needs no Go runtime.

Required:

- Ubuntu or another modern Linux distribution
- `bash`

Optional:

- `ripgrep` for faster large-repository search (`find` has a built-in fallback)
- `script` from `util-linux` for PTY commands
- Node.js 20+ and `unshare` for sandboxed JavaScript `exec`
- the relevant language server(s) for `code_intel`
- Chromium/Chrome for browser tools
- OpenAI `tunnel-client` for ChatGPT Secure MCP Tunnel
- `cloudflared` for the built-in Cloudflare quick-tunnel option
- a working `systemd --user` instance for persistent autostart

Ubuntu basics:

```bash
sudo apt update
sudo apt install -y ripgrep util-linux
```

Check your machine:

```bash
cos doctor
```

## Install

From the release directory:

```bash
./install.sh
```

This installs `cos` to `~/.local/bin` by default and enables the default automatic-start policy. If no projects exist yet, no daemon is started until you add the first project.

Or manually on x86-64:

```bash
install -Dm755 dist/cos-linux-amd64 ~/.local/bin/cos
```

## First run

Open the TUI:

```bash
cos
```

Press `a`, enter a project path, and press Enter. The project is persisted in `~/.config/cos-lite/config.json`.

**Autostart defaults on.** Adding the first enabled project automatically reconciles the background daemon. On Ubuntu with a working user systemd manager, `cos` installs/enables `cos-lite.service`. It also makes a best-effort call to `loginctl enable-linger`, which allows the user service to start during boot before login when system policy permits it. If lingering is unavailable, the service still starts with the user session/login.

Check exactly which mode your machine is using:

```bash
cos service status
```

With multiple projects, model-facing paths are explicit virtual roots:

```text
/trendseer/backend/main.py
/mdc/src/train.py
/foundationdb/fdbserver/...
```

Relative paths are intentionally rejected when multiple roots are enabled, so a model cannot accidentally operate on the wrong repository.

## TUI controls

| Key | Action |
|---|---|
| `↑` / `↓`, `j` / `k` | Select project |
| `space` | Enable/disable selected project |
| `a` | Add project |
| `d` | Remove project from config, never delete files |
| `s` | Start/stop daemon |
| `r` | Restart daemon |
| `t` | Tunnel setup |
| `i` | Show discovered Skills |
| `o` | Live MCP Activity / OpenTelemetry view |
| `l` | Daemon logs |
| `b` | Enable/disable browser MCP tools |
| `c` | Enable/disable semantic code intelligence |
| `u` | Toggle automatic startup policy |
| `q` | Quit TUI only |

Adding, removing, enabling, or disabling a project reconciles the daemon immediately. With autostart enabled, adding the first project also starts the service automatically. Disabling/removing the final exposed project stops the daemon cleanly while keeping the autostart policy ready for the next enabled project.

When the daemon is already running, project exposure, Skills, CodeIntel, Codex MCP-server configuration, and tunnel configuration are hot-reloaded without replacing the daemon process. Existing terminal sessions, MCP Tasks, plan handles, browser contexts, audit history, and the listening MCP socket survive ordinary reloads. Changing the configured listen address still requires an explicit daemon restart. Changing browser enable/headless settings can rotate the browser pool because those settings change the Chromium runtime itself.

## CLI control

```bash
cos start
cos stop
cos restart
cos status
cos endpoint
cos logs 100
cos audit 100
cos audit --json 100

cos project list
cos project add ~/Desktop/intern/trendseer trendseer
cos project disable trendseer
cos project enable trendseer
cos project remove trendseer

cos skill list
cos skill add ./review-pr
cos skill remove review-pr

cos code-intel status
cos code-intel enable
cos code-intel disable

cos tunnel status
cos tunnel set none
cos tunnel key-from-file /secure/path/runtime-key
cos tunnel set openai tunnel_0123456789abcdef0123456789abcdef
cos tunnel set cloudflare
cos tunnel set custom 'my-tunnel --forward {local_url}'

cos service enable
cos service disable
cos service status
```

`cos endpoint` prints the OpenAI `tunnel_id` when the OpenAI provider is selected, the public URL for URL-based tunnels, or the local tokenized MCP URL when tunneling is disabled. `cos status` deliberately redacts the local path token.

## Tunnel setup

The TUI `t` screen supports OpenAI Secure MCP Tunnel, Cloudflare quick tunnel, a custom command, and disabled mode.

### OpenAI Secure MCP Tunnel

Install OpenAI's `tunnel-client` and make sure `tunnel-client` is on `PATH`. The current client requires a tunnel ID, a runtime control-plane API key, and an MCP target. cos-lite supplies its own local tokenized MCP URL as the target and supervises `tunnel-client` as a child of the cos-lite daemon.

In the TUI:

1. Run `cos` and press `t`.
2. Choose `OpenAI Secure MCP Tunnel`.
3. Enter the `tunnel_...` ID from OpenAI Tunnels management.
4. Enter the Runtime API key created with Tunnels Read + Use permission.
5. cos-lite hot-reloads the tunnel child and waits for tunnel-client's `/readyz` before showing the tunnel as connected. The MCP daemon itself stays running.

The runtime key is never written to `config.json` or passed in process arguments. It is stored separately at:

```text
~/.config/cos-lite/openai-tunnel.key
```

with mode `0600`, and tunnel-client receives only `--control-plane.api-key=file:...`. The tunnel ID is stored in normal cos-lite config because it is an identifier rather than a secret.

CLI setup is also available without putting the API key in shell history:

```bash
cos tunnel key-from-file /secure/path/openai-runtime-key
cos tunnel set openai tunnel_0123456789abcdef0123456789abcdef
cos tunnel status
```

When this provider is active, the existing `cos-lite.service` manages both the MCP daemon and its `tunnel-client` child, so there is no second systemd service to maintain.

One OpenAI `tunnel_id` must have only one local tunnel-client runtime. Running original Chat On Steroids and cos-lite against the same tunnel ID can split MCP requests between different local servers. cos-lite detects an already-running local tunnel-client using the configured tunnel ID and refuses to start that duplicate runtime with a clear error.

### Cloudflare/custom tunnels

A custom command can use:

- `{local_url}`: full local MCP URL including its secret path
- `{origin}`: loopback HTTP origin without the MCP path

Example:

```bash
cos tunnel set custom 'my-tunnel --url {local_url}'
```

If the command prints an `https://...` URL, `cos-lite` captures it for `cos endpoint` and the TUI status screen.

## Automatic startup and boot behavior

The policy is enabled by default. You can explicitly manage it with:

```bash
cos service enable
cos service disable
cos service status
```

The user unit is stored at:

```text
~/.config/systemd/user/cos-lite.service
```

`cos service status` reports four distinct facts:

```text
policy: true
enabled: true
active: true
linger: true
```

`linger: true` means the user manager can start at boot before login. `linger: false` means ordinary user-service startup still works at login/session start.

## One-off / stdio mode

Persistent projects are for the background daemon. One-off mode remains available:

```bash
cos serve /absolute/path/to/project
```

Or stdio:

```bash
cos serve --transport stdio --browser=false /absolute/path/to/project
```

Disable optional schemas in one-off mode if desired:

```bash
cos serve --skills=false --code-intel=false --browser=false /path/to/project
```

## Browser behavior

Browser tools use isolated Chromium contexts. Omitting `browser_context_id` uses a stable per-client default context, which keeps older cached connector schemas working. `browser_tabs` returns that context's random `browser_...` handle; pass it explicitly for portable stateless use. Set `new_context: true` on `browser_tabs` to mint an additional context. Each handle maps to a dedicated Chromium profile and ephemeral loopback CDP port. No extension is installed and your normal browser profile is not reused.

Browser context handles are bearer capabilities. Separate chats normally mint different handles and therefore operate separate Chromium profiles; deliberately copying a valid `browser_context_id` to another chat grants access to that transient browser context. Client metadata is not an authentication boundary.

## Audit and OpenTelemetry

Every MCP request records a bounded audit event containing only operational metadata: timestamp, duration, diagnostic client label, method/tool/resource category, status, trace ID, and span ID. Tool arguments, command text, file contents, API keys, session/plan/browser/task capability handles, and MCP path tokens are not written to the audit record.

Inspect it with:

```bash
cos audit 100
cos audit --json 100
```

The JSONL file is mode `0600` and rotates at a bounded size:

```text
~/.local/state/cos-lite/audit.jsonl
```

Set a standard OTLP/HTTP endpoint to export spans without installing an OpenTelemetry SDK dependency:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=https://collector.example.com
export OTEL_EXPORTER_OTLP_HEADERS='Authorization=Bearer ...'
export OTEL_SERVICE_NAME=cos-lite
```

cos-lite posts OTLP JSON to `/v1/traces`, propagates HTTP `traceparent`/`tracestate`, and drops exporter work if the bounded async queue is full rather than blocking MCP requests.

### TUI Activity view

Press `o` in the TUI to open the live **MCP Activity / OpenTelemetry** screen. It shows recent operations with timestamp, status, client/chat label, tool/resource/task name, latency, trace ID and span ID. The selected event also shows a short textual output preview.

Controls:

```text
↑ / k    older event
↓ / j    newer event
g        jump to latest
Enter/q  return
```

Output previews are intentionally **not** added to `audit.jsonl` and are **not** exported through OTLP. They are stored only in a separate local mode-`0600` bounded file used by the TUI:

```text
~/.local/state/cos-lite/activity.jsonl
```

The preview is capped at about 1.2k characters, ignores image/audio/base64 content, strips terminal control sequences, and redacts obvious bearer tokens, API keys, passwords, secrets and MCP path tokens. Because arbitrary tool output can still contain sensitive source code or application data, treat `activity.jsonl` as private local state.

## Plugin configuration

Default file:

```text
~/.codex/config.toml            # external MCP servers (shared with Codex)
```

Example:

```json
[
  {
    "name": "example",
    "command": ["example-mcp", "--stdio"]
  }
]
```

An upstream tool named `search` is published as `example.search`.

## JavaScript `exec`

When Node 20+ and unprivileged namespaces are available, `exec` can compose local tools:

```js
const file = await tools.read({ paths: ["/trendseer/README.md"] });
const refs = await tools.code_intel({
  action: "references",
  path: "/trendseer/backend/service.go",
  line: 42,
  column: 10
});
return { file, refs };
```

The JavaScript process receives no direct filesystem or child-process grants. On Linux it is additionally placed in fresh user/network/PID namespaces. If the namespace sandbox is unavailable, JavaScript mode fails closed; declarative `exec.calls` remains available.

## Security model

The daemon binds to loopback (`127.0.0.1:8766` by default). v0.3.2+ migrates the old unversioned `127.0.0.1:8765` default because that port conflicts with Chat On Steroids when both are installed. Its MCP endpoint contains a persistent random path token stored mode `0600` in `~/.config/cos-lite/http-token`. HTTP Origin validation reduces DNS-rebinding risk.

The OpenAI tunnel runtime API key is stored separately in `~/.config/cos-lite/openai-tunnel.key` with mode `0600`. It is passed to tunnel-client by `file:` reference, never embedded in the tunnel command line or normal cos-lite configuration.

Filesystem tools canonicalize configured roots, resolve symlinks, reject root escapes, and require explicit project names when multiple roots are enabled.

Transient state uses opaque bearer-capability handles (`sess_...`, `plan_...`, `browser_...`, `task_...`) rather than trusting self-reported MCP client metadata. Keep those handles private. This prevents accidental cross-workflow enumeration and interference, but it is not a substitute for user authentication or OS-level multi-tenant isolation. If cos-lite is ever exposed to mutually untrusted users, use proper MCP/OAuth authorization and a dedicated Unix account/container/VM boundary.

Skills are local instructions. Discovery refuses symlinked Skill directories/files, package imports are bounded, and model-facing metadata uses standard `.agents/skills` paths rather than cos-lite-specific skill locations.

Language servers are trusted local executables. `code_intel` launches them with the project as the working directory and strips common LLM/MCP API secrets from their environment, but an LSP process still runs with your Unix account privileges.

`exec_command` is intentionally powerful. It starts in a validated project directory, but the shell itself runs with your Unix account privileges and can access anything that account can access. For stronger isolation, run `cos-lite` under a dedicated user/container/VM.

Daemon state/logs:

```text
~/.local/state/cos-lite/
```

The same directory contains `audit.jsonl`; audit events intentionally exclude tool arguments and file contents.

Configuration and managed Skills:

```text
~/.config/cos-lite/
```

## Build and test

Requires Go 1.23+.

```bash
make test
make smoke
make dist
```

`make smoke` runs MCP, Skills, process, Codex-configured external MCP proxying, browser/CDP, persistent control-plane hot reload, OpenAI tunnel lifecycle, and multi-chat isolation/Tasks/Resources/AGENTS/audit tests. Unit tests also run an in-process fake LSP protocol server; release validation additionally exercises installed language servers when available.

## License

MIT. See `LICENSE`.
