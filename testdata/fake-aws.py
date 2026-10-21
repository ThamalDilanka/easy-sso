#!/usr/bin/python3
"""Test double only. Never imported or selected by the production executable."""
import datetime, fcntl, json, os, socket, sys, time
from pathlib import Path
root = Path(__file__).resolve().parent
args = sys.argv[1:]
if args == ['--version']:
    print('aws-cli/2.37.0 TEST-DOUBLE'); sys.exit(0)
mode = json.loads((root/'mode.json').read_text()) if (root/'mode.json').exists() else {}
def arg(name, default=''):
    return args[args.index(name)+1] if name in args else default
def transaction(fn):
    with open(root/'sessions.json', 'a+') as f:
        fcntl.flock(f, fcntl.LOCK_EX); f.seek(0)
        text=f.read(); data=json.loads(text) if text else []
        result=fn(data); f.seek(0); f.truncate(); json.dump(data,f); f.flush()
        fcntl.flock(f, fcntl.LOCK_UN); return result
with open(root/'calls.jsonl','a') as f:
    json.dump({'args':args,'ambientProvider':any(k in os.environ for k in ['AWS_PROFILE','AWS_DEFAULT_PROFILE','AWS_ROLE_ARN','AWS_WEB_IDENTITY_TOKEN_FILE','AWS_ENDPOINT_URL'])},f); f.write('\n')
def identity(key):
    second=key=='ASIA_TEST_two'
    return {'Account':'222222222222' if second else '111111111111','Arn':'arn:aws:sts::222222222222:assumed-role/OrganizationAccountAccessRole/test' if second else 'arn:aws:sts::111111111111:assumed-role/AWSReservedSSO_ReadOnlyAccess_0123456789abcdef/test','UserId':'TEST:two' if second else 'TEST:one'}
if args[:2]==['sso','login']:
    print('TEST official login browser opened'); sys.exit(0)
if args[:2]==['sso','logout']:
    sys.exit(1 if mode.get('logoutFailure') else 0)
if args[:2]==['configure','export-credentials']:
    name=arg('--profile');
    if mode.get('exportFailure')==name: sys.exit(1)
    print(json.dumps({'Version':1,'AccessKeyId':'ASIA_TEST_'+name,'SecretAccessKey':'SECRET_TEST_ONLY','SessionToken':'TOKEN_TEST_ONLY','Expiration':(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(hours=1)).isoformat()})); sys.exit(0)
if args[:2]==['sts','get-caller-identity']:
    data=identity(os.environ.get('AWS_ACCESS_KEY_ID'))
    if mode.get('identityMismatch'): data['Account']='000000000000'
    print(json.dumps(data));sys.exit(0)
if args[:2]==['ssm','describe-sessions']:
    if mode.get('metadataFailure'): sys.exit(1)
    if mode.get('unconfirmed'): print('{"Sessions":[]}');sys.exit(0)
    filters=json.loads(arg('--filters','[]'))
    def match(row):
        return all(row.get({'Target':'Target','Owner':'Owner','SessionId':'SessionId'}.get(f['key'],''),f['value'])==f['value'] for f in filters if f['key'] in ['Target','Owner','SessionId'])
    statuses = ['Terminated', 'Failed'] if arg('--state') == 'History' else ['Connected', 'Connecting', 'Disconnected', 'Terminating']
    data=transaction(lambda rows:[dict(r) for r in rows if match(r) and r['Status'] in statuses])
    print(json.dumps({'Sessions':data}));sys.exit(0)
if args[:2]==['ssm','terminate-session']:
    target=arg('--session-id')
    def end(rows):
        for r in rows:
            if r['SessionId']==target:r['Status']='Terminated'
    transaction(end);print('{}');sys.exit(0)
if args[:2]==['ssm','start-session']:
    if mode.get('startDenied'):sys.exit(1)
    sid='session-'+arg('--reason').split(':')[-1]
    params=json.loads(arg('--parameters','{}'))
    listener=None
    if params and mode.get('pluginGrandchild'):
        # Like the real plugin: a grandchild in the aws process group holds the
        # port and the output pipe, and never watches its parent.
        ready,signal_ready=os.pipe()
        if os.fork()==0:
            try:
                os.close(ready)
                held=socket.socket();held.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
                held.bind(('127.0.0.1',int(params['localPortNumber'][0])));held.listen()
                (root/'plugin.pid.tmp').write_text('%d %d'%(os.getpid(),os.getpgid(0)));os.replace(root/'plugin.pid.tmp',root/'plugin.pid')
                os.write(signal_ready,b'1');os.close(signal_ready)
                while True:time.sleep(1)
            finally:
                os._exit(1)
        os.close(signal_ready)
        if os.read(ready,1)!=b'1':sys.exit(1)
        os.close(ready)
    elif params:
        listener=socket.socket();listener.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
        listener.bind(('0.0.0.0' if mode.get('publicListener') else '127.0.0.1',int(params['localPortNumber'][0])));listener.listen()
    row={'SessionId':sid,'Target':arg('--target'),'Reason':arg('--reason'),'Owner':identity(os.environ.get('AWS_ACCESS_KEY_ID'))['Arn'],'StartDate':time.time(),'Status':'Connected'}
    transaction(lambda rows:rows.append(row))
    if mode.get('terminalEcho'):
        print('EMBEDDED READY',flush=True)
    if mode.get('brokenPipe'):
        # The real plugin prints this on stdout when its websocket write fails.
        print('EMBEDDED READY',flush=True)
        while not (root/'broken-pipe').exists():
            time.sleep(.025)
        print('Cannot perform start session: write tcp 10.0.0.2:46944->10.0.0.3:443: write: broken pipe',flush=True)
        if listener:listener.close()
        sys.exit(1)
    while True:
        active=transaction(lambda rows:any(r['SessionId']==sid and r['Status']=='Connected' for r in rows))
        if not active:break
        if mode.get('terminalEcho'):
            import select
            if select.select([sys.stdin],[],[],0)[0]:
                line=sys.stdin.readline()
                print('REMOTE:'+line.strip(),flush=True)
        time.sleep(.025)
    if listener:listener.close()
    sys.exit(0)
print('unsupported TEST call',file=sys.stderr);sys.exit(2)
