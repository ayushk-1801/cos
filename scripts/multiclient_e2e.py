#!/usr/bin/env python3
import json, os, pathlib, subprocess, tempfile, time, urllib.request, urllib.error

BIN = os.path.abspath(os.sys.argv[1] if len(os.sys.argv) > 1 else './cos')
P = '2026-07-28'

def run(env, *args):
    return subprocess.run([BIN, *args], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)

def rpc(url, client, req_id, method, params=None, name=None, tasks=True):
    params = dict(params or {})
    caps = {'extensions': {'io.modelcontextprotocol/tasks': {}}} if tasks else {}
    params['_meta'] = {
        'io.modelcontextprotocol/protocolVersion': P,
        'io.modelcontextprotocol/clientInfo': {'name': 'ChatGPT', 'version': 'e2e'},
        'io.modelcontextprotocol/clientCapabilities': caps,
        'openai/session': client,
    }
    body = json.dumps({'jsonrpc': '2.0', 'id': req_id, 'method': method, 'params': params}).encode()
    headers = {
        'Content-Type': 'application/json',
        'Accept': 'application/json, text/event-stream',
        'MCP-Protocol-Version': P,
        'Mcp-Method': method,
    }
    if name is not None:
        headers['Mcp-Name'] = name
    req = urllib.request.Request(url, data=body, method='POST', headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.load(resp)
    except urllib.error.HTTPError as e:
        return json.loads(e.read())

def tool(url, client, req_id, name, args, tasks=True):
    return rpc(url, client, req_id, 'tools/call', {'name': name, 'arguments': args}, name=name, tasks=tasks)

def read_resource(url, client, req_id, uri, tasks=True):
    return rpc(url, client, req_id, 'resources/read', {'uri': uri}, name=uri, tasks=tasks)

def content_text(result):
    return result['result']['contents'][0]['text']

with tempfile.TemporaryDirectory() as td:
    td = pathlib.Path(td)
    home = td / 'home'; cfgd = td / 'config'; stated = td / 'state'; project = td / 'project'; extra = td / 'extra'
    for p in (home, cfgd, stated, project, extra): p.mkdir()
    (project / 'AGENTS.md').write_text('root instruction: keep changes focused\n')
    nested = project / 'src' / 'pkg'; nested.mkdir(parents=True)
    (project / 'src' / 'AGENTS.md').write_text('nested instruction: run focused tests\n')
    (nested / 'x.go').write_text('package pkg\n')
    subprocess.run(['git', '-C', str(project), 'init', '-q'], check=True)
    subprocess.run(['git', '-C', str(project), 'config', 'user.email', 'cos-e2e@example.test'], check=True)
    subprocess.run(['git', '-C', str(project), 'config', 'user.name', 'cos-e2e'], check=True)
    subprocess.run(['git', '-C', str(project), 'add', '.'], check=True)
    subprocess.run(['git', '-C', str(project), 'commit', '-qm', 'initial'], check=True)
    env = dict(os.environ, HOME=str(home), XDG_CONFIG_HOME=str(cfgd), XDG_STATE_HOME=str(stated), COS_DISABLE_SYSTEMD='1')
    run(env, 'service', 'enable')
    config_path = cfgd / 'cos-lite' / 'config.json'
    cfg = json.loads(config_path.read_text()); cfg['listen'] = '127.0.0.1:0'; cfg['browser'] = False; config_path.write_text(json.dumps(cfg, indent=2) + '\n')
    run(env, 'project', 'add', str(project), 'proj')
    try:
        sp = stated / 'cos-lite' / 'state.json'; deadline = time.time() + 5
        while time.time() < deadline:
            if sp.exists():
                state = json.loads(sp.read_text())
                if state.get('endpoint'): break
            time.sleep(.05)
        else: raise AssertionError('daemon state not ready')
        url = state['endpoint']

        discover = rpc(url, 'client-a', 1, 'server/discover')
        disc = discover['result']
        assert 'resources' in disc['capabilities'], disc
        assert 'io.modelcontextprotocol/tasks' in disc['capabilities']['extensions'], disc
        assert 'root instruction' in disc['instructions'], disc['instructions']

        status_a = json.loads(content_text(read_resource(url, 'client-a', 2, 'cos://status')))
        status_b = json.loads(content_text(read_resource(url, 'client-b', 3, 'cos://status')))
        key_a = status_a['client']['key']; key_b = status_b['client']['key']
        assert key_a != key_b, (key_a, key_b)

        listed = rpc(url, 'client-a', 4, 'resources/list')['result']['resources']
        uris = {r['uri'] for r in listed}
        for uri in ('cos://status', 'cos://projects', 'cos://skills', 'cos://audit/recent', 'cos://projects/proj/instructions'):
            assert uri in uris, (uri, uris)
        nested_text = content_text(read_resource(url, 'client-a', 5, 'cos://projects/proj/instructions?path=src/pkg/x.go'))
        assert 'root instruction' in nested_text and 'nested instruction' in nested_text, nested_text
        git_ctx = json.loads(content_text(read_resource(url, 'client-a', 52, 'cos://projects/proj/git')))
        assert git_ctx['isGit'] is True and git_ctx['head'] and git_ctx['recentCommits'], git_ctx
        nested_read = tool(url, 'client-a', 51, 'read', {'path': '/proj/src/pkg/x.go'})['result']
        nested_result_text = '\n'.join(item.get('text', '') for item in nested_read['content'] if item.get('type') == 'text')
        assert 'Applicable nested AGENTS.md' in nested_result_text and 'nested instruction' in nested_result_text, nested_read

        started = tool(url, 'client-a', 6, 'exec_command', {
            'cmd': "bash -c 'echo READY; read x; echo GOT:$x; sleep 0.2'",
            'workdir': '/proj', 'yield_time_ms': 10, 'tty': True,
        })['result']
        sid = started['structuredContent']['session_id']
        assert sid.startswith('sess_') and len(sid) == len('sess_') + 32, sid
        unknown_session = tool(url, 'client-b', 7, 'write_stdin', {'session_id': 'sess_' + '0'*32, 'chars': 'bad\n', 'yield_time_ms': 20})['result']
        assert unknown_session.get('isError') and 'unknown session_id' in unknown_session['content'][0]['text'], unknown_session
        own = tool(url, 'client-a', 9, 'write_stdin', {'session_id': sid, 'chars': 'good\n', 'yield_time_ms': 1000})['result']
        assert 'GOT:good' in own['content'][0]['text'], own

        other_session = tool(url, 'client-b', 10, 'exec_command', {
            'cmd': "bash -c 'read x; echo OTHER:$x'", 'workdir': '/proj', 'yield_time_ms': 10, 'tty': True,
        })['result']['structuredContent']['session_id']
        assert other_session != sid and other_session.startswith('sess_'), (sid, other_session)
        other_done = tool(url, 'client-b', 11, 'write_stdin', {'session_id': other_session, 'chars': 'b\n', 'yield_time_ms': 1000})['result']
        assert 'OTHER:b' in other_done['content'][0]['text'], other_done

        # Stateful handles and the listener must survive ordinary config hot
        # reloads. Keep a PTY blocked on stdin while adding another project.
        hot = tool(url, 'client-a', 70, 'exec_command', {
            'cmd': "bash -c 'echo HOTREADY; read x; echo HOT:$x'",
            'workdir': '/proj', 'yield_time_ms': 10, 'tty': True,
        })['result']
        hot_sid = hot['structuredContent']['session_id']
        hot_task = tool(url, 'client-a', 74, 'exec_command', {
            'cmd': "sleep 0.45; printf 'task-survived-reload\\n'", 'workdir': '/proj', 'yield_time_ms': 1,
        })['result']
        assert hot_task['resultType'] == 'task', hot_task
        hot_task_id = hot_task['taskId']
        before_state = json.loads(sp.read_text()); before_pid = before_state['pid']; before_endpoint = before_state['endpoint']
        (extra / 'extra.txt').write_text('extra-hot-reload\n')
        run(env, 'project', 'add', str(extra), 'extra')
        deadline = time.time() + 4
        while time.time() < deadline:
            reloaded = json.loads(sp.read_text())
            if 'extra' in reloaded.get('projects', []): break
            time.sleep(.05)
        else: raise AssertionError('hot reload did not expose extra project')
        assert reloaded['pid'] == before_pid, (before_state, reloaded)
        assert reloaded['endpoint'] == before_endpoint, (before_state, reloaded)
        resumed = tool(url, 'client-a', 72, 'write_stdin', {'session_id': hot_sid, 'chars': 'reload-ok\n', 'yield_time_ms': 1000})['result']
        assert 'HOT:reload-ok' in resumed['content'][0]['text'], resumed
        extra_read = tool(url, 'client-a', 73, 'read', {'path': '/extra/extra.txt'})['result']
        assert 'extra-hot-reload' in extra_read['content'][0]['text'], extra_read
        deadline = time.time() + 4
        while time.time() < deadline:
            task_after_reload = rpc(url, 'client-a', 75, 'tasks/get', {'taskId': hot_task_id}, name=hot_task_id)['result']
            if task_after_reload['status'] == 'completed': break
            time.sleep(.05)
        else: raise AssertionError('task did not survive config hot reload')
        assert 'task-survived-reload' in task_after_reload['result']['content'][0]['text'], task_after_reload

        plan_a = tool(url, 'client-a', 81, 'update_plan', {'plan': [{'step': 'A', 'status': 'in_progress'}]})['result']['structuredContent']['plan_id']
        plan_b = tool(url, 'client-b', 82, 'update_plan', {'plan': [{'step': 'B', 'status': 'pending'}]})['result']['structuredContent']['plan_id']
        assert plan_a != plan_b and plan_a.startswith('plan_') and plan_b.startswith('plan_'), (plan_a, plan_b)
        wrong_plan = tool(url, 'client-b', 83, 'update_plan', {'plan_id': 'plan_' + '0'*32, 'plan': [{'step': 'bad', 'status': 'pending'}]})['result']
        assert wrong_plan.get('isError') and 'unknown plan_id' in wrong_plan['content'][0]['text'], wrong_plan
        plan_update = tool(url, 'client-a', 85, 'update_plan', {'plan_id': plan_a, 'plan': [{'step': 'A', 'status': 'completed'}]})['result']
        assert plan_update['structuredContent']['plan_id'] == plan_a, plan_update

        created = tool(url, 'client-a', 90, 'exec_command', {
            'cmd': "sleep 0.12; printf 'task-final-output\\n'", 'workdir': '/proj', 'yield_time_ms': 1,
        })['result']
        assert created['resultType'] == 'task', created
        task_id = created['taskId']
        assert task_id.startswith('task_') and len(task_id) == len('task_') + 32, task_id
        unknown = rpc(url, 'client-b', 91, 'tasks/get', {'taskId': 'task_' + '0'*32}, name='task_' + '0'*32)
        assert unknown['error']['code'] == -32602 and 'unknown taskId' in unknown['error']['message'], unknown
        other = rpc(url, 'client-b', 92, 'tasks/get', {'taskId': task_id}, name=task_id)
        assert other['result']['taskId'] == task_id and other['result']['status'] in ('working', 'completed'), other
        missing_cap = rpc(url, 'client-a', 93, 'tasks/get', {'taskId': task_id}, name=task_id, tasks=False)
        assert missing_cap['error']['code'] == -32021, missing_cap
        deadline = time.time() + 4
        while time.time() < deadline:
            got = rpc(url, 'client-a', 94, 'tasks/get', {'taskId': task_id}, name=task_id)['result']
            if got['status'] == 'completed': break
            time.sleep(.05)
        else: raise AssertionError('task did not complete')
        assert 'task-final-output' in got['result']['content'][0]['text'], got

        audit = json.loads(content_text(read_resource(url, 'client-a', 95, 'cos://audit/recent')))
        audit_keys = {e['client_key'] for e in audit}
        assert key_a in audit_keys and key_b not in audit_keys, audit
        assert any(e['method'] == 'tools/call' and e.get('target') == 'exec_command' for e in audit), audit

        raw_audit = stated / 'cos-lite' / 'audit.jsonl'
        assert raw_audit.exists() and (raw_audit.stat().st_mode & 0o777) == 0o600
        raw_events = [json.loads(line) for line in raw_audit.read_text().splitlines() if line.strip()]
        raw_keys = {e['client_key'] for e in raw_events}
        assert key_a in raw_keys and key_b in raw_keys, raw_keys
        assert 'task-final-output' not in raw_audit.read_text(), 'tool output leaked into audit.jsonl'

        activity_file = stated / 'cos-lite' / 'activity.jsonl'
        assert activity_file.exists() and (activity_file.stat().st_mode & 0o777) == 0o600
        deadline = time.time() + 1.5
        activity_events = []
        while time.time() < deadline:
            activity_events = [json.loads(line) for line in activity_file.read_text().splitlines() if line.strip()]
            if any(e.get('method') == 'tasks/get' and 'task-final-output' in e.get('output_preview', '') for e in activity_events):
                break
            time.sleep(.03)
        assert any(e.get('target') == 'read' and 'package pkg' in e.get('output_preview', '') for e in activity_events), activity_events[-10:]
        assert any(e.get('method') == 'tasks/get' and 'task-final-output' in e.get('output_preview', '') for e in activity_events), activity_events[-10:]
        print('[ok] capability isolation, hot reload, Tasks, Resources, AGENTS.md, multi-client audit and local activity previews')
    finally:
        subprocess.run([BIN, 'stop'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
