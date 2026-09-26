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

with tempfile.TemporaryDirectory() as td:
    td=pathlib.Path(td); home=td/'home'; cfgd=td/'config'; stated=td/'state'; home.mkdir();cfgd.mkdir();stated.mkdir()
    p1=td/'alpha';p2=td/'beta';p1.mkdir();p2.mkdir();(p1/'a.txt').write_text('alpha\n');(p2/'b.txt').write_text('beta\n')
    env=dict(os.environ,HOME=str(home),XDG_CONFIG_HOME=str(cfgd),XDG_STATE_HOME=str(stated))
    run(env,'project','add',str(p1),'alpha')
    auto=run(env,'status').stdout; assert 'daemon: running' in auto and 'projects: alpha' in auto,auto
    run(env,'project','add',str(p2),'beta')
    config_path=cfgd/'cos-lite'/'config.json'; cfg=json.loads(config_path.read_text());cfg['listen']='127.0.0.1:0';cfg['browser']=False;cfg['tunnel']={'provider':'custom','command':"echo ready https://control.example.test; sleep 20"};config_path.write_text(json.dumps(cfg,indent=2)+'\n')
    run(env,'restart')
    try:
        sp=stated/'cos-lite'/'state.json';deadline=time.time()+5
        while time.time()<deadline:
            if sp.exists():
                state=json.loads(sp.read_text())
                if state.get('public_url'): break
            time.sleep(.05)
        else: raise AssertionError('state/public tunnel URL not ready')
        assert state['projects']==['alpha','beta'],state
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
        assert state.get('public_url'),state
        out=run(env,'status').stdout;assert 'projects: alpha' in out,out
        endpoint=run(env,'endpoint').stdout.strip();assert endpoint.startswith('https://control.example.test/mcp/'),endpoint
        log=run(env,'logs','20').stdout;assert 'started; projects=alpha' in log or 'started; projects=alpha,beta' in log,log
        # Disabling the final exposed project must stop cleanly rather than restart-loop.
        run(env,'project','disable','alpha')
        stopped=run(env,'status').stdout; assert 'daemon: stopped' in stopped,stopped
        listed=run(env,'project','list').stdout; assert 'alpha' in listed and 'disabled' in listed,listed
        run(env,'project','enable','alpha'); run(env,'start')
        restarted=run(env,'status').stdout; assert 'daemon: running' in restarted and 'projects: alpha' in restarted,restarted
        print('[ok] TUI control-plane daemon/projects/tunnel lifecycle')
    finally:
        subprocess.run([BIN,'stop'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
