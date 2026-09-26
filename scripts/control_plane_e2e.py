#!/usr/bin/env python3
import json, os, pathlib, subprocess, tempfile, time, urllib.request

BIN = os.path.abspath(os.sys.argv[1] if len(os.sys.argv)>1 else './cos')
P='2026-07-28'

def run(env,*args):
    return subprocess.run([BIN,*args],env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,check=True)

def call(url,i,name,args):
    params={'name':name,'arguments':args,'_meta':{'io.modelcontextprotocol/protocolVersion':P,'io.modelcontextprotocol/clientInfo':{'name':'control-e2e','version':'1'},'io.modelcontextprotocol/clientCapabilities':{}}}
    body=json.dumps({'jsonrpc':'2.0','id':i,'method':'tools/call','params':params}).encode()
    req=urllib.request.Request(url,data=body,headers={'Content-Type':'application/json','Accept':'application/json','MCP-Protocol-Version':P,'Mcp-Method':'tools/call','Mcp-Name':name},method='POST')
    return json.load(urllib.request.urlopen(req,timeout=10))['result']

def tool_names(url):
    params={'_meta':{'io.modelcontextprotocol/protocolVersion':P,'io.modelcontextprotocol/clientInfo':{'name':'control-e2e','version':'1'},'io.modelcontextprotocol/clientCapabilities':{}}}
    body=json.dumps({'jsonrpc':'2.0','id':99,'method':'tools/list','params':params}).encode()
    req=urllib.request.Request(url,data=body,headers={'Content-Type':'application/json','Accept':'application/json','MCP-Protocol-Version':P,'Mcp-Method':'tools/list'},method='POST')
    return [t['name'] for t in json.load(urllib.request.urlopen(req,timeout=10))['result']['tools']]

with tempfile.TemporaryDirectory() as td:
    td=pathlib.Path(td); home=td/'home'; cfgd=td/'config'; stated=td/'state'; codex=td/'codex'; home.mkdir();cfgd.mkdir();stated.mkdir();codex.mkdir()
    p1=td/'alpha';p2=td/'beta';p1.mkdir();p2.mkdir();(p1/'a.txt').write_text('alpha\n');(p2/'b.txt').write_text('beta\n')
    env=dict(os.environ,HOME=str(home),XDG_CONFIG_HOME=str(cfgd),XDG_STATE_HOME=str(stated),CODEX_HOME=str(codex),COS_DISABLE_SYSTEMD='1')
    # Reserve no fixed host port in the integration test. This still exercises
    # automatic first-project startup, but cannot collide with another local app.
    run(env,'service','enable')
    config_path=cfgd/'cos-lite'/'config.json'; cfg=json.loads(config_path.read_text());cfg['listen']='127.0.0.1:0';cfg['browser']=False;config_path.write_text(json.dumps(cfg,indent=2)+'\n')
    run(env,'project','add',str(p1),'alpha')
    auto=run(env,'status').stdout; assert 'daemon: running' in auto and 'projects: alpha' in auto,auto
    sp=stated/'cos-lite'/'state.json'; initial_state=json.loads(sp.read_text()); daemon_pid=initial_state['pid']
    run(env,'project','add',str(p2),'beta')
    deadline=time.time()+4
    while time.time()<deadline:
        state=json.loads(sp.read_text())
        if state.get('projects')==['alpha','beta']: break
        time.sleep(.05)
    assert state['projects']==['alpha','beta'],state
    assert state['pid']==daemon_pid,('project add restarted daemon',daemon_pid,state)

    fake=td/'fake_mcp.py'
    fake.write_text("""import json,sys
for line in sys.stdin:
 msg=json.loads(line); rid=msg.get('id'); method=msg.get('method')
 if rid is None: continue
 if method=='server/discover': result={'resultType':'complete','supportedVersions':['2026-07-28'],'capabilities':{'tools':{}},'ttlMs':1000,'cacheScope':'public'}
 elif method=='tools/list': result={'resultType':'complete','tools':[{'name':'echo','description':'echo','inputSchema':{'type':'object','properties':{}}}],'ttlMs':1000,'cacheScope':'public'}
 elif method=='tools/call': result={'resultType':'complete','content':[{'type':'text','text':'ok'}]}
 else:
  print(json.dumps({'jsonrpc':'2.0','id':rid,'error':{'code':-32601,'message':'unsupported'}}),flush=True); continue
 print(json.dumps({'jsonrpc':'2.0','id':rid,'result':result}),flush=True)
""")
    codex_cfg=codex/'config.toml'
    codex_cfg.write_text('[mcp_servers.fake]\ncommand = '+json.dumps(os.sys.executable)+'\nargs = ["-u", '+json.dumps(str(fake))+']\n')
    deadline=time.time()+5
    while time.time()<deadline:
        state=json.loads(sp.read_text())
        if 'fake.echo' in tool_names(state['endpoint']): break
        time.sleep(.05)
    else: raise AssertionError('Codex MCP config hot reload did not add fake.echo')
    assert state['pid']==daemon_pid,('Codex MCP add restarted daemon',daemon_pid,state)
    codex_cfg.write_text('')
    deadline=time.time()+5
    while time.time()<deadline:
        state=json.loads(sp.read_text())
        if 'fake.echo' not in tool_names(state['endpoint']): break
        time.sleep(.05)
    else: raise AssertionError('Codex MCP config hot reload did not remove fake.echo')
    assert state['pid']==daemon_pid,('Codex MCP remove restarted daemon',daemon_pid,state)

    cfg=json.loads(config_path.read_text());cfg['tunnel']={'provider':'custom','command':"echo ready https://control.example.test; sleep 20"};config_path.write_text(json.dumps(cfg,indent=2)+'\n')
    try:
        deadline=time.time()+5
        while time.time()<deadline:
            if sp.exists():
                state=json.loads(sp.read_text())
                if state.get('public_url'): break
            time.sleep(.05)
        else: raise AssertionError('state/public tunnel URL not ready')
        assert state['projects']==['alpha','beta'],state
        assert state['pid']==daemon_pid,('tunnel config hot reload restarted daemon',daemon_pid,state)
        assert state['public_url'].endswith('/mcp/'+state['endpoint'].rsplit('/',1)[1]),state
        assert 'alpha' in call(state['endpoint'],1,'read',{'path':'/alpha/a.txt'})['content'][0]['text']
        assert 'beta' in call(state['endpoint'],2,'read',{'path':'/beta/b.txt'})['content'][0]['text']
        amb=call(state['endpoint'],3,'read',{'path':'a.txt'})['content'][0]['text'];assert 'path must name' in amb,amb
        run(env,'project','disable','beta')
        state=json.loads(sp.read_text()); deadline=time.time()+4
        while time.time()<deadline:
            state=json.loads(sp.read_text())
            if state.get('projects')==['alpha'] and state.get('public_url'): break
            time.sleep(.05)
        assert state['projects']==['alpha'],state
        assert state['pid']==daemon_pid,('project disable restarted daemon',daemon_pid,state)
        assert state.get('public_url'),state
        out=run(env,'status').stdout;assert 'projects: alpha' in out,out
        endpoint=run(env,'endpoint').stdout.strip();assert endpoint.startswith('https://control.example.test/mcp/'),endpoint
        compact_log=run(env,'logs','20').stdout; assert 'Started v' in compact_log and 'project' in compact_log,compact_log
        raw_log=run(env,'logs','--raw','20').stdout;assert 'started; projects=alpha' in raw_log or 'started; projects=alpha,beta' in raw_log,raw_log
        # Disabling the final exposed project must stop cleanly rather than restart-loop.
        run(env,'project','disable','alpha')
        stopped=run(env,'status').stdout; assert 'daemon: stopped' in stopped,stopped
        listed=run(env,'project','list').stdout; assert 'alpha' in listed and 'disabled' in listed,listed
        run(env,'project','enable','alpha'); run(env,'start')
        restarted=run(env,'status').stdout; assert 'daemon: running' in restarted and 'projects: alpha' in restarted,restarted
        print('[ok] TUI control-plane daemon/projects/tunnel lifecycle')
    finally:
        subprocess.run([BIN,'stop'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)

with tempfile.TemporaryDirectory() as td:
    td=pathlib.Path(td); home=td/'home'; cfgd=td/'config'; stated=td/'state'; bind=td/'bin'; project=td/'project'
    for p in (home,cfgd,stated,bind,project): p.mkdir()
    fake=bind/'tunnel-client'
    fake.write_text(r'''#!/usr/bin/env python3
import http.server, os, signal, socketserver, sys
health_file=''
for arg in sys.argv[1:]:
    if arg.startswith('--health.url-file='):
        health_file=arg.split('=',1)[1]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == '/readyz':
            self.send_response(200); self.end_headers(); self.wfile.write(b'ready')
        else:
            self.send_response(404); self.end_headers()
    def log_message(self,*args): pass
srv=socketserver.TCPServer(('127.0.0.1',0),H)
open(health_file,'w').write('http://127.0.0.1:%d\n' % srv.server_address[1])
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
srv.serve_forever()
''')
    fake.chmod(0o755)
    key=td/'runtime.key'; key.write_text('sk-runtime-e2e\n'); key.chmod(0o600)
    env=dict(os.environ,HOME=str(home),XDG_CONFIG_HOME=str(cfgd),XDG_STATE_HOME=str(stated),COS_DISABLE_SYSTEMD='1',PATH=str(bind)+os.pathsep+os.environ.get('PATH',''))
    run(env,'service','enable')
    config_path=cfgd/'cos-lite'/'config.json'; cfg=json.loads(config_path.read_text());cfg['listen']='127.0.0.1:0';cfg['browser']=False;config_path.write_text(json.dumps(cfg,indent=2)+'\n')
    run(env,'project','add',str(project),'project')
    try:
        run(env,'tunnel','key-from-file',str(key))
        tunnel_id='tunnel_0123456789abcdef0123456789abcdef'
        run(env,'tunnel','set','openai',tunnel_id)
        sp=stated/'cos-lite'/'state.json';deadline=time.time()+6
        while time.time()<deadline:
            if sp.exists():
                state=json.loads(sp.read_text())
                if state.get('tunnel')=='connected': break
            time.sleep(.05)
        else: raise AssertionError('OpenAI tunnel did not become ready: '+(sp.read_text() if sp.exists() else 'no state'))
        assert run(env,'endpoint').stdout.strip()==tunnel_id
        status=run(env,'tunnel','status').stdout
        assert 'provider: openai' in status and 'runtime_key: configured' in status and tunnel_id in status,status
        secret_path=cfgd/'cos-lite'/'openai-tunnel.key'
        assert secret_path.exists() and (secret_path.stat().st_mode & 0o777)==0o600
        cfg_text=config_path.read_text(); assert 'sk-runtime-e2e' not in cfg_text
        print('[ok] OpenAI Secure MCP Tunnel CLI/readiness lifecycle')
    finally:
        subprocess.run([BIN,'stop'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
