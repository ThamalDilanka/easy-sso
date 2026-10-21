#!/usr/bin/python3
"""PTY user journey through the real Bubble Tea executable with fake AWS."""
import fcntl, json, os, pty, re, select, signal, socket, struct, subprocess, sys, termios, time
from pathlib import Path
exe, root, fake = sys.argv[1:4]
scenario = sys.argv[4] if len(sys.argv) > 4 else 'journey'
root=Path(root); state=root/'tui-state'; config=root/'config'
plugin=root/'session-manager-plugin';plugin.write_text('#!/bin/sh\nprintf "1.2.0 TEST-DOUBLE\\n"\n');plugin.chmod(0o700)
env=dict(os.environ, HOME=str(root), XDG_CONFIG_HOME=str(root/'xdg'), AWS_CONFIG_FILE=str(config), AWS_SHARED_CREDENTIALS_FILE=str(root/'tui-credentials'), TERM='xterm-256color', NO_COLOR='1', EASY_SSO_ASCII='1', PATH=str(root)+':'+os.environ.get('PATH',''))
master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',35,140,0,0))
p=subprocess.Popen([exe,'--state-dir',str(state),'--aws-cli',fake],stdin=slave,stdout=slave,stderr=slave,env=env,start_new_session=True);os.close(slave)
output='';position=0
ansi=re.compile(r'\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))')
def wait(text,seconds=12):
    global output,position
    end=time.monotonic()+seconds
    while time.monotonic()<end:
        if text in ansi.sub('',output[position:]):position=len(output);return
        ready,_,_=select.select([master],[],[],.05)
        if ready:
            try:output+=os.read(master,65536).decode(errors='replace')
            except OSError:break
        if p.poll() is not None:break
    raise AssertionError('Missing '+repr(text)+' in TUI output tail: '+repr(ansi.sub('',output)[-4000:]))
def send(text):os.write(master,text.encode());time.sleep(.08)
def open_tunnel():
    send('2');send('n');send('p');send('app-db\r');send('db.example.rds.amazonaws.com\r');send('\x15eu-west-1\r');send('\r');send('i-0123456789abcdef0\r');send('\r')
    probe=socket.socket();probe.bind(('127.0.0.1',0));port=probe.getsockname()[1];probe.close()
    send('\x15'+str(port)+'\r');send('\x15tunnel\r');send('\r');send('\r');send('\r');wait('Review connection');send('\r');wait('Tunnel open')
    return port
def drain(seconds):
    global output
    if select.select([master],[],[],seconds)[0]:
        try:output+=os.read(master,65536).decode(errors='replace')
        except OSError:time.sleep(seconds)
def alive(pid):
    # A zombie holds no port; only its new parent can reap it.
    try:return Path('/proc/%d/stat'%pid).read_text().rsplit(')',1)[1].split()[0]!='Z'
    except (FileNotFoundError,ProcessLookupError):return False
def port_free(port):
    s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
    try:s.bind(('127.0.0.1',port));return True
    except OSError:return False
    finally:s.close()
def plugin_ids():
    return tuple(map(int,(root/'plugin.pid').read_text().split()))
def hangup():
    # Closing the terminal window sends SIGHUP to easysso. The normal quit path
    # must end the tunnel: the plugin grandchild exits and the port is free.
    wait('ReadOnlyAccess')
    send('\x01');wait('AWS CLI SSO cleanup complete')
    port=open_tunnel()
    pid,_=plugin_ids()
    assert alive(pid) and not port_free(port), 'fake plugin grandchild is not holding the tunnel port'
    os.kill(p.pid,signal.SIGHUP)
    end=time.monotonic()+5
    while time.monotonic()<end and (alive(pid) or not port_free(port) or p.poll() is None):drain(.05)
    assert not alive(pid), 'plugin outlived the closed terminal window'
    assert port_free(port), 'tunnel port still held after the terminal window closed'
    assert p.poll()==0, 'easysso did not quit normally on SIGHUP: %r'%p.poll()
    sessions=json.loads((root/'sessions.json').read_text())
    assert sessions and all(r['Status']=='Terminated' for r in sessions), 'remote session was not ended by the normal quit path'
    assert 'SECRET_TEST_ONLY' not in output and 'TOKEN_TEST_ONLY' not in output
    print('PASS: SIGHUP quits through shutdown, ends the remote session and the plugin grandchild, and frees the tunnel port')
if scenario=='hangup':
    try:hangup()
    finally:
        if p.poll() is None:p.kill();p.wait()
        # Never leave the fake plugin or its aws parent behind, even on failure.
        if (root/'plugin.pid').exists():
            pid,pgid=plugin_ids()
            for kill,target in ((os.killpg,pgid),(os.kill,pid)):
                try:kill(target,signal.SIGKILL)
                except (ProcessLookupError,PermissionError):pass
        os.close(master)
    sys.exit(0)
try:
    wait('ReadOnlyAccess')
    original_config=config.read_text()
    config.write_text(original_config.replace('sso_role_name=ReadOnlyAccess', 'sso_role_name=TemporaryReadOnlyAccess'))
    send('\x12');wait('Role: TemporaryReadOnlyAccess')
    config.write_text(original_config)
    send('\x12');wait('Role: ReadOnlyAccess')
    send('\x01');wait('AWS CLI SSO cleanup complete')
    open_tunnel()
    bookmarks=json.loads((state/'connections.json').read_text());assert len(bookmarks['connections'])==1
    c=bookmarks['connections'][0];assert c['region']=='eu-west-1' and c['relayRegion']=='us-east-1' and c['lastUsedAt']
    send('4');send('d');send('\r');wait('Stop selected session complete')
    send('2');send('v');wait('Connection details');send('\x1b');send('\r');wait('Tunnel open')
    send('4');send('d');send('\r');wait('Stop selected session complete')
    send('6');send('e');send('\r');wait('Export bookmarks complete');assert (state/'bookmarks-backup.json').exists()
    send('i');send('\r');wait('Import preview: merge review');send('\r');wait('Import bookmarks complete')
    send('5');send('d');send('\r');wait('Remove managed role complete')
    send('q');send('\r');p.wait(timeout=10);assert p.returncode==0
    assert 'SECRET_TEST_ONLY' not in output and 'TOKEN_TEST_ONLY' not in output
    print('PASS: PTY dynamic config reload, activation, DB create/review and saved Enter connect/v details/stop, backup export/import, credential removal, normal exit')
finally:
    if p.poll() is None:p.kill();p.wait()
    os.close(master)
