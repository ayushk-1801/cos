#!/usr/bin/env python3
"""Binary-level cos-lite smoke test. Uses only Python's standard library."""

import argparse
import base64
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

PROTOCOL = "2026-07-28"
PNG_1X1 = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9WlJwAAAAASUVORK5CYII="
)


def rpc(url, request_id, method, params=None, name=None):
    params = dict(params or {})
    params["_meta"] = {
        "io.modelcontextprotocol/protocolVersion": PROTOCOL,
        "io.modelcontextprotocol/clientInfo": {"name": "cos-lite-e2e", "version": "1"},
        "io.modelcontextprotocol/clientCapabilities": {},
    }
    body = json.dumps(
        {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}
    ).encode()
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
        "MCP-Protocol-Version": PROTOCOL,
        "Mcp-Method": method,
    }
    if name:
        headers["Mcp-Name"] = name
    req = urllib.request.Request(url, data=body, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read())


def call(url, request_id, name, arguments):
    status, response = rpc(
        url,
        request_id,
        "tools/call",
        {"name": name, "arguments": arguments},
        name,
    )
    assert status == 200, (status, response)
    assert "error" not in response, response
    return response["result"]


def start_http(binary, workspace, *extra):
    env = os.environ.copy()
    codex_home = Path(workspace) / ".codex-e2e"
    codex_home.mkdir(parents=True, exist_ok=True)
    env["CODEX_HOME"] = str(codex_home)
    proc = subprocess.Popen(
        [
            binary,
            "--transport",
            "http",
            "--listen",
            "127.0.0.1:0",
            "--token",
            "none",
            *extra,
            workspace,
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
    )
    deadline = time.time() + 8
    seen = []
    while time.time() < deadline:
        line = proc.stderr.readline()
        if line:
            seen.append(line.rstrip())
            match = re.search(r"MCP: (http://\S+)", line)
            if match:
                return proc, match.group(1)
        elif proc.poll() is not None:
            break
    proc.terminate()
    raise AssertionError(f"server failed to start: {seen}")


def stop(proc):
    if proc.poll() is not None:
        return
    proc.terminate()
    try:
        proc.wait(timeout=4)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=2)


def core_http(binary):
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        (root / "hello.txt").write_text("alpha\nbeta\ngamma\n")
        (root / "pixel.png").write_bytes(PNG_1X1)
        skill_dir = root / ".agents" / "skills" / "smoke"
        skill_dir.mkdir(parents=True)
        (skill_dir / "SKILL.md").write_text("---\nname: Smoke Skill\ndescription: Verify skill discovery.\n---\n# Smoke\nUse read and tests.\n")
        proc, url = start_http(binary, td, "--browser=false")
        try:
            status, discovered = rpc(url, 1, "server/discover")
            assert status == 200, discovered
            assert discovered["result"]["resultType"] == "complete", discovered
            assert "2025-11-25" in discovered["result"]["supportedVersions"], discovered

            status, listed = rpc(url, 2, "tools/list")
            assert status == 200, listed
            names = {tool["name"] for tool in listed["result"]["tools"]}
            expected = {
                "read",
                "view_image",
                "find",
                "apply_patch",
                "exec_command",
                "write_stdin",
                "update_plan",
                "skills",
                "code_intel",
                "exec",
            }
            assert expected <= names, expected - names

            result = call(url, 3, "read", {"path": "hello.txt", "start_line": 2, "end_line": 3})
            text = result["content"][0]["text"]
            assert "beta" in text and "gamma" in text, text

            result = call(url, 4, "view_image", {"path": "pixel.png"})
            image = next(item for item in result["content"] if item.get("type") == "image")
            assert image["mimeType"] == "image/png" and len(image["data"]) > 20, result

            result = call(url, 5, "find", {"query": "beta", "path": "."})
            assert "hello.txt" in result["content"][0]["text"], result

            patch = """*** Begin Patch
*** Update File: hello.txt
@@
 alpha
-beta
+BETA
 gamma
*** Add File: added.txt
+new file
*** End Patch"""
            result = call(url, 6, "apply_patch", {"patch": patch})
            assert not result.get("isError"), result
            assert (root / "hello.txt").read_text() == "alpha\nBETA\ngamma\n"
            assert (root / "added.txt").read_text() == "new file\n"

            result = call(url, 7, "exec_command", {"cmd": "printf ok", "yield_time_ms": 1000})
            assert result["structuredContent"]["exit_code"] == 0, result
            assert "ok" in result["structuredContent"]["output"], result

            result = call(
                url,
                8,
                "exec_command",
                {
                    "cmd": 'read line; printf "got:%s\\n" "$line"; sleep 0.1',
                    "yield_time_ms": 40,
                },
            )
            session_id = result["structuredContent"].get("session_id")
            assert session_id, result
            result = call(
                url,
                9,
                "write_stdin",
                {"session_id": session_id, "chars": "abc\n", "yield_time_ms": 1000},
            )
            assert "got:abc" in result["structuredContent"]["output"], result

            if shutil.which("script"):
                result = call(
                    url,
                    10,
                    "exec_command",
                    {"cmd": "printf tty-ok", "tty": True, "yield_time_ms": 1000},
                )
                assert result["structuredContent"]["exit_code"] == 0, result
                assert "tty-ok" in result["structuredContent"]["output"], result

            result = call(
                url,
                11,
                "update_plan",
                {
                    "plan": [
                        {"step": "inspect", "status": "completed"},
                        {"step": "finish", "status": "in_progress"},
                    ]
                },
            )
            assert "inspect" in result["content"][0]["text"], result

            result = call(url, 12, "skills", {"action": "list"})
            assert any(s["id"].endswith("/smoke") for s in result["structuredContent"]["skills"]), result
            result = call(url, 13, "skills", {"action": "get", "id": "smoke"})
            assert "# Smoke" in result["content"][0]["text"], result

            result = call(
                url,
                14,
                "exec",
                {
                    "calls": [
                        {"tool": "read", "arguments": {"path": "added.txt"}},
                        {"tool": "find", "arguments": {"query": "BETA", "path": "."}},
                    ]
                },
            )
            assert not result.get("isError"), result

            # JavaScript exec is optional only when the host disables user namespaces.
            if shutil.which("node") and shutil.which("unshare"):
                namespace_probe = subprocess.run(
                    ["unshare", "-Urn", "true"],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
                if namespace_probe.returncode == 0:
                    result = call(
                        url,
                        15,
                        "exec",
                        {
                            "code": 'const r=await tools.find({query:"BETA",path:"."}); return {matches:r.structuredContent.matches};'
                        },
                    )
                    assert not result.get("isError"), result
                    assert result["structuredContent"]["matches"] >= 1, result

            print(f"[ok] core HTTP ({len(names)} tools)")
        finally:
            stop(proc)


def stdio(binary):
    with tempfile.TemporaryDirectory() as td:
        Path(td, "x.txt").write_text("stdio\n")
        env = os.environ.copy()
        codex_home = Path(td) / ".codex-e2e"
        codex_home.mkdir(parents=True, exist_ok=True)
        env["CODEX_HOME"] = str(codex_home)
        proc = subprocess.Popen(
            [binary, "--transport", "stdio", "--browser=false", td],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env=env,
        )
        requests = [
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "server/discover",
                "params": {
                    "_meta": {
                        "io.modelcontextprotocol/protocolVersion": PROTOCOL,
                        "io.modelcontextprotocol/clientCapabilities": {},
                    }
                },
            },
            {
                "jsonrpc": "2.0",
                "id": 2,
                "method": "tools/list",
                "params": {
                    "_meta": {
                        "io.modelcontextprotocol/protocolVersion": PROTOCOL,
                        "io.modelcontextprotocol/clientCapabilities": {},
                    }
                },
            },
        ]
        for request in requests:
            proc.stdin.write(json.dumps(request) + "\n")
            proc.stdin.flush()
        responses = [json.loads(proc.stdout.readline()), json.loads(proc.stdout.readline())]
        assert responses[0]["result"]["resultType"] == "complete", responses[0]
        assert any(tool["name"] == "read" for tool in responses[1]["result"]["tools"]), responses[1]
        proc.stdin.close()
        proc.wait(timeout=5)
        assert proc.returncode == 0, proc.stderr.read()
        print("[ok] stdio transport")


def plugin_proxy(binary):
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        plugin = root / "fake_plugin.py"
        plugin.write_text(
            """import json,sys
for line in sys.stdin:
    msg=json.loads(line); rid=msg.get('id'); method=msg.get('method')
    if rid is None: continue
    if method=='server/discover':
        result={'resultType':'complete','supportedVersions':['2026-07-28'],'capabilities':{'tools':{}},'ttlMs':1000,'cacheScope':'public'}
    elif method=='tools/list':
        result={'resultType':'complete','tools':[{'name':'echo','description':'echo','inputSchema':{'type':'object','properties':{'text':{'type':'string'}}}}],'ttlMs':1000,'cacheScope':'public'}
    elif method=='tools/call':
        result={'resultType':'complete','content':[{'type':'text','text':msg['params']['arguments'].get('text','')} ]}
    else:
        print(json.dumps({'jsonrpc':'2.0','id':rid,'error':{'code':-32601,'message':'unsupported'}}),flush=True); continue
    print(json.dumps({'jsonrpc':'2.0','id':rid,'result':result}),flush=True)
"""
        )
        codex_home = root / ".codex-e2e"
        codex_home.mkdir(parents=True, exist_ok=True)
        command = json.dumps(sys.executable)
        script = json.dumps(str(plugin))
        (codex_home / "config.toml").write_text(
            f'[mcp_servers.fake]\ncommand = {command}\nargs = ["-u", {script}]\n'
        )
        proc, url = start_http(binary, td, "--browser=false")
        try:
            status, listed = rpc(url, 30, "tools/list")
            assert status == 200, listed
            assert any(tool["name"] == "fake.echo" for tool in listed["result"]["tools"]), listed
            result = call(url, 31, "fake.echo", {"text": "plugin-ok"})
            assert result["content"][0]["text"] == "plugin-ok", result
            print("[ok] Codex-configured external MCP proxy")
        finally:
            stop(proc)


def browser(binary):
    chromium = next(
        (
            shutil.which(name)
            for name in ("chromium", "chromium-browser", "google-chrome", "google-chrome-stable")
            if shutil.which(name)
        ),
        None,
    )
    if not chromium:
        print("[skip] browser CDP (Chromium/Chrome not installed)")
        return
    with tempfile.TemporaryDirectory() as td:
        proc, url = start_http(binary, td)
        try:
            result = call(url, 40, "browser_tabs", {"action": "list"})
            browser_context_id = result["structuredContent"]["browser_context_id"]
            tabs = result["structuredContent"]["tabs"]
            assert browser_context_id.startswith("browser_") and len(browser_context_id) == len("browser_") + 32, result
            assert tabs, result
            original = tabs[0]["id"]

            # Explicitly mint a second opaque browser capability backed by a
            # separate Chromium profile/process.
            result2 = call(url, 401, "browser_tabs", {"action": "list", "new_context": True})
            browser_context_id_2 = result2["structuredContent"]["browser_context_id"]
            tabs2 = result2["structuredContent"]["tabs"]
            assert browser_context_id_2 != browser_context_id, (browser_context_id, browser_context_id_2)
            assert tabs2, result2
            assert not ({t["id"] for t in tabs} & {t["id"] for t in tabs2}), (tabs, tabs2)
            original2 = tabs2[0]["id"]
            call(url, 402, "browser_evaluate", {"browser_context_id": browser_context_id, "tab_id": original, "expression": "document.title='context-one'"})
            call(url, 403, "browser_evaluate", {"browser_context_id": browser_context_id_2, "tab_id": original2, "expression": "document.title='context-two'"})
            title1 = call(url, 404, "browser_evaluate", {"browser_context_id": browser_context_id, "tab_id": original, "expression": "document.title"})
            title2 = call(url, 405, "browser_evaluate", {"browser_context_id": browser_context_id_2, "tab_id": original2, "expression": "document.title"})
            assert title1["structuredContent"] == "context-one", title1
            assert title2["structuredContent"] == "context-two", title2

            # Legacy/cached client schemas that do not know browser_context_id
            # still operate on the stable per-client default context.
            legacy_title = call(url, 406, "browser_evaluate", {"tab_id": original, "expression": "document.title"})
            assert legacy_title["structuredContent"] == "context-one", legacy_title

            created = call(url, 41, "browser_tabs", {"action": "new", "browser_context_id": browser_context_id, "url": "about:blank"})
            assert created["structuredContent"]["browser_context_id"] == browser_context_id, created
            tab_id = created["structuredContent"]["tab"]["id"]
            call(url, 42, "browser_navigate", {"browser_context_id": browser_context_id, "tab_id": tab_id, "url": "about:blank"})
            call(
                url,
                43,
                "browser_evaluate",
                {
                    "browser_context_id": browser_context_id,
                    "tab_id": tab_id,
                    "expression": "(()=>{document.body.innerHTML='<input id=\"i\"><button id=\"b\">go</button>';document.querySelector('#b').onclick=()=>console.log('hit');return document.title='E2E'})()",
                },
            )
            result = call(url, 44, "browser_snapshot", {"browser_context_id": browser_context_id, "tab_id": tab_id})
            assert "#i" in json.dumps(result), result
            call(
                url,
                45,
                "browser_action",
                {"browser_context_id": browser_context_id, "tab_id": tab_id, "action": "fill", "selector": "#i", "value": "hello"},
            )
            result = call(
                url,
                46,
                "browser_evaluate",
                {"browser_context_id": browser_context_id, "tab_id": tab_id, "expression": "document.querySelector('#i').value"},
            )
            assert result["structuredContent"] == "hello", result
            call(url, 47, "browser_action", {"browser_context_id": browser_context_id, "tab_id": tab_id, "action": "click", "selector": "#b"})
            result = call(url, 48, "browser_console", {"browser_context_id": browser_context_id, "tab_id": tab_id})
            assert "consoleAPICalled" in json.dumps(result), result
            result = call(url, 49, "browser_network", {"browser_context_id": browser_context_id, "tab_id": tab_id})
            assert isinstance(result["structuredContent"], list), result
            result = call(url, 50, "browser_screenshot", {"browser_context_id": browser_context_id, "tab_id": tab_id})
            image = next(item for item in result["content"] if item.get("type") == "image")
            assert len(image["data"]) > 100, result
            call(url, 51, "browser_tabs", {"action": "close", "browser_context_id": browser_context_id, "tab_id": tab_id})
            # The initial tab should remain reachable after closing the test tab.
            result = call(url, 52, "browser_tabs", {"action": "list", "browser_context_id": browser_context_id})
            assert any(tab["id"] == original for tab in result["structuredContent"]["tabs"]), result
            print("[ok] Chromium CDP browser tools + isolated browser contexts")
        finally:
            stop(proc)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", nargs="?", default="./cos")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    if not os.path.isfile(binary) or not os.access(binary, os.X_OK):
        raise SystemExit(f"binary is not executable: {binary}")
    core_http(binary)
    stdio(binary)
    plugin_proxy(binary)
    browser(binary)
    print("ALL_E2E_SMOKE_TESTS_PASSED")


if __name__ == "__main__":
    main()
