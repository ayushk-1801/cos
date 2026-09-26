# Security

`cos-lite` exposes powerful local capabilities to an MCP client. Treat possession of its tokenized MCP URL as sensitive, and treat enabled `exec_command` access as shell access under the Unix account running the daemon.

## Defaults

- Persistent HTTP binds to loopback (`127.0.0.1`) by default.
- One-off non-loopback binds are refused unless `--allow-remote` is explicitly supplied.
- `--token none` is refused on non-loopback listeners.
- The HTTP path contains a random token stored at `~/.config/cos-lite/http-token` with mode `0600`.
- HTTP requests with a non-loopback `Origin` are rejected to reduce DNS-rebinding risk.
- Every configured project is canonicalized to a separate approved root.
- With more than one project enabled, file and workdir paths must explicitly name `/project/...`.
- File tools resolve symlinks and reject paths that escape the selected root.
- Mutating file operations reject a final symlink.
- Child command and LSP environments strip common LLM/MCP API secrets.
- Long-running commands use their own Linux process group so timeout/cancellation reaches descendants.
- JavaScript `exec` uses Node's permission model plus Linux user/network/PID namespaces and fails closed when the namespace sandbox is unavailable.
- Chromium control uses a dedicated profile and loopback CDP port; no browser extension is installed.
- Daemon status output redacts the local MCP token. `cos endpoint` intentionally reveals the usable URL on request.

## Skills

Skills are instructions, not capabilities. A Skill cannot grant filesystem/shell/browser access that the MCP server has not already exposed.

Managed Skill packages are bounded by file/byte limits. Symlinks are refused during discovery/import, and supporting-file reads are constrained to the selected Skill directory. Model-facing Skill metadata uses `/skills/...` virtual paths rather than native config paths.

A Skill can still contain bad advice or malicious shell instructions. Only install Skills from sources you trust, and rely on the same command/file security policy you would use for any model-authored action.

## Code intelligence

`code_intel` starts locally installed language servers such as `gopls`, `clangd`, `rust-analyzer`, Pyright, or `typescript-language-server`.

The LSP is not a kernel sandbox. It runs with the Unix account's privileges and may inspect files outside the project if the language server itself chooses to. `cos-lite` strips common LLM/MCP API secrets from its environment and only sends project/document paths requested through approved roots, but you should treat installed language servers as trusted local software.

The `rename` action is preview-only. `cos-lite` does not automatically apply the WorkspaceEdit returned by the language server.

## Tunnels

The built-in Cloudflare mode forwards only the loopback origin and retains the secret MCP path in the public URL. A custom tunnel command receives `{local_url}` or `{origin}` only if you explicitly configure it.

Tunnel URLs and MCP path tokens should be treated like passwords. Do not paste them into public issues or logs.

## Automatic startup

The default policy enables a systemd user service after the first project is added. `cos-lite` makes a best-effort `loginctl enable-linger` call so the service can start at boot before login. This does not grant root privileges to the daemon; the service still runs as your user.

Disable automatic startup with:

```bash
cos service disable
```

## Important limitation

`exec_command` is intentionally a shell. The approved-project sandbox applies to MCP file tools and to the command's starting directory, **not** to arbitrary filesystem operations performed by the shell command. A model can run commands with the same privileges as the Unix account running `cos-lite`.

For stronger isolation, run `cos-lite` under a dedicated Unix account, container, VM, or an additional kernel sandbox with only the directories you intend to expose.
