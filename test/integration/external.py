#!/usr/bin/env python3
"""Opt-in transport probe: SSH VDS <-> a disposable, non-host-network Pod.

Requires working SSH key authentication, a built bin/k0nnect, and wireguard-tools,
iproute2 and ping on the VDS. Installs tools only inside the disposable Pod.
The Kubernetes fields in the inventory are transport-test fixtures: this script
does NOT join a worker, validate a CNI profile, or migrate existing node addresses.
All test resources and private keys are removed in finally; packages on the VDS
are not installed or removed by this script.
"""
import argparse
import base64
import hashlib
import json
import pathlib
import shlex
import signal
import subprocess
import sys
import time


def execute(args, data=None, check=True, timeout=180):
    result = subprocess.run(args, input=data, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"command failed ({result.returncode}): "
                           f"{result.stdout.decode(errors='replace')} "
                           f"{result.stderr.decode(errors='replace')}")
    return result


SERVER = '''import socket, threading, sys
address = sys.argv[1]
def tcp():
    s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
    s.bind((address,18080)); s.listen(8)
    while True:
        c,_=s.accept(); c.settimeout(10)
        with c:
            data=b''
            while len(data)<65536:
                block=c.recv(65536-len(data))
                if not block: break
                data+=block
            c.sendall(data)
def udp():
    s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.bind((address,18081))
    while True:
        data,peer=s.recvfrom(4096); s.sendto(data,peer)
threading.Thread(target=tcp,daemon=True).start()
udp()
'''
CLIENT = '''import socket,sys,json,time
address=sys.argv[1]; payload=bytes(i%251 for i in range(65536))
last=None
for _ in range(10):
    try:
        s=socket.create_connection((address,18080),timeout=5); break
    except OSError as e:
        last=e; time.sleep(.5)
else: raise last
with s:
    s.sendall(payload); data=b''
    while len(data)<len(payload):
        part=s.recv(len(payload)-len(data))
        if not part: break
        data+=part
assert data==payload, 'TCP payload mismatch'
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.settimeout(5)
s.sendto(payload[:1000],(address,18081)); data,_=s.recvfrom(4096)
assert data==payload[:1000], 'UDP payload mismatch'
print(json.dumps({'peer':address,'tcpBytes':65536,'udpBytes':1000,'passed':True}))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--kubeconfig', required=True)
    parser.add_argument('--kubectl', default='kubectl')
    parser.add_argument('--ssh-host', required=True)
    parser.add_argument('--known-hosts', required=True)
    parser.add_argument('--vds-address', required=True)
    parser.add_argument('--cluster-node', required=True)
    parser.add_argument('--cluster-address', required=True)
    parser.add_argument('--report', required=True)
    args = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parents[2]
    binary = (root/'bin/k0nnect').read_bytes()
    def terminate(_signal, _frame):
        raise KeyboardInterrupt('test interrupted')
    signal.signal(signal.SIGTERM, terminate)
    stamp = str(time.time_ns())
    namespace = 'k0nnect-transport-'+stamp
    k = [args.kubectl, '--kubeconfig', args.kubeconfig, '--request-timeout=20s']
    ssh = ['ssh', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=accept-new',
           '-o', 'UserKnownHostsFile='+args.known_hosts, '-o', 'ConnectTimeout=10',
           args.ssh_host]
    report = {'scope':'isolated-transport-only; no worker join or CNI certification',
              'binarySHA256':hashlib.sha256(binary).hexdigest(),
              'namespace':namespace, 'clusterNode':args.cluster_node,
              'vdsAddress':args.vds_address, 'clusterAddress':args.cluster_address,
              'checks':[], 'cleanupErrors':[]}
    remote_dir = None
    created = False
    applied_vds = False
    applied_pod = False

    def phase(message):
        print(message, flush=True)

    def remote(script, check=True):
        return execute(ssh+['bash', '-se'], script.encode(), check=check)

    def pod(command, data=None, check=True):
        return execute(k+['-n',namespace,'exec','-i','probe','--']+command,
                       data=data, check=check)

    def put_remote(path, data, mode='0600'):
        # stdin may contain secrets; never interpolate it into a command/log.
        return execute(ssh+['sh', '-ec', shlex.quote(
            'umask 077; cat > '+shlex.quote(path)+'; chmod '+mode+' '+shlex.quote(path))], data)

    def vds_command(command, check=True):
        line = (shlex.quote(remote_dir+'/k0nnect')+' '+command+
                ' --config '+shlex.quote(remote_dir+'/inventory.json')+' --node vds-probe')
        return remote(line, check=check)

    def pod_command(command, check=True):
        return pod(['/tmp/k0nnect']+shlex.split(command)+[
            '--config','/tmp/inventory.json','--node','cluster-probe'], check=check)

    try:
        phase('Create isolated probe Pod and UDP NodePort')
        ns = {'apiVersion':'v1','kind':'Namespace','metadata':{'name':namespace,
              'labels':{'app.kubernetes.io/managed-by':'k0nnect-integration-test',
                        'pod-security.kubernetes.io/enforce':'privileged'}}}
        execute(k+['create','-f','-'], json.dumps(ns).encode())
        created = True
        install = ('apt-get update -qq && apt-get install -y --no-install-recommends '
                   'wireguard-tools iproute2 iputils-ping python3 && '
                   'touch /tmp/k0nnect-ready && exec sleep infinity')
        resources = {'apiVersion':'v1','kind':'List','items':[
            {'apiVersion':'v1','kind':'Pod','metadata':{'name':'probe','namespace':namespace,
             'labels':{'app':'k0nnect-transport-probe'}},
             'spec':{'nodeName':args.cluster_node,'automountServiceAccountToken':False,
             'containers':[{'name':'probe','image':'ubuntu:24.04',
                'command':['bash','-ec',install],
                'env':[{'name':'DEBIAN_FRONTEND','value':'noninteractive'}],
                'securityContext':{'allowPrivilegeEscalation':False,
                    'capabilities':{'add':['NET_ADMIN','NET_RAW']}},
                'resources':{'requests':{'cpu':'25m','memory':'64Mi'},
                             'limits':{'cpu':'500m','memory':'384Mi'}},
                'readinessProbe':{'exec':{'command':['test','-f','/tmp/k0nnect-ready']},
                                   'periodSeconds':2}}]}},
            {'apiVersion':'v1','kind':'Service','metadata':{'name':'wireguard','namespace':namespace},
             'spec':{'type':'NodePort','externalTrafficPolicy':'Local',
                'selector':{'app':'k0nnect-transport-probe'},
                'ports':[{'name':'wireguard','protocol':'UDP','port':31820,
                          'targetPort':31820,'nodePort':31820}]}}
        ]}
        execute(k+['apply','-f','-'],json.dumps(resources).encode())
        execute(k+['-n',namespace,'wait','--for=condition=Ready','pod/probe','--timeout=150s'],timeout=170)
        phase('Create local keys and upload binary; no private keys leave their host')
        remote_dir = remote('mktemp -d /root/k0nnect-transport.XXXXXXXX').stdout.decode().strip()
        if not remote_dir.startswith('/root/k0nnect-transport.') or '\n' in remote_dir:
            raise RuntimeError('unexpected remote directory')
        put_remote(remote_dir+'/k0nnect',binary,'0700')
        pod(['sh','-ec','umask 077; cat > /tmp/k0nnect; chmod 0700 /tmp/k0nnect'],binary)
        vds_public = remote('umask 077; wg genkey > '+shlex.quote(remote_dir+'/node.key')+
                            '; wg pubkey < '+shlex.quote(remote_dir+'/node.key')).stdout.decode().strip()
        pod_public = pod(['sh','-ec','umask 077; wg genkey > /tmp/node.key; wg pubkey < /tmp/node.key']).stdout.decode().strip()
        for key in [vds_public,pod_public]:
            if len(base64.b64decode(key,validate=True))!=32: raise RuntimeError('invalid public key')
        inventory = json.loads((root/'examples/inventory.json').read_text())
        inventory['cluster']='transport-probe'
        inventory['transport'].update(interface='k0nprobe0',cidr='10.203.73.0/24')
        # Synthetic Kubernetes profile required by the current driver constructor.
        # No command in this harness uses Kubernetes join or changes real CNI state.
        inventory['kubernetes']['apiServer']='10.203.73.1:6443'
        inventory['nodes']=[
            {'name':'vds-probe','address':'10.203.73.1','endpoint':args.vds_address+':31821',
             'wireguard':{'publicKey':vds_public,'privateKeyFile':remote_dir+'/node.key'}},
            {'name':'cluster-probe','address':'10.203.73.2','endpoint':args.cluster_address+':31820',
             'wireguard':{'publicKey':pod_public,'privateKeyFile':'/tmp/node.key'}}]
        encoded=json.dumps(inventory).encode()
        put_remote(remote_dir+'/inventory.json',encoded)
        pod(['sh','-ec','umask 077; cat > /tmp/inventory.json'],encoded)
        phase('Preflight and reconcile the isolated WireGuard link')
        vds_command('doctor'); pod_command('doctor')
        applied_vds=True; vds_command('apply')
        applied_pod=True; pod_command('apply')
        vds_command('verify'); pod_command('verify')
        report['checks'].append({'name':'authenticated-handshake-and-1400-byte-DF-ping-both-directions','passed':True})
        phase('TCP 64KiB and UDP 1000-byte echo in both directions through WireGuard')
        put_remote(remote_dir+'/echo.py',SERVER.encode())
        pod(['sh','-ec','cat > /tmp/echo.py'],SERVER.encode())
        remote('nohup python3 '+shlex.quote(remote_dir+'/echo.py')+' 10.203.73.1 > '+
               shlex.quote(remote_dir+'/echo.log')+' 2>&1 < /dev/null & echo $! > '+
               shlex.quote(remote_dir+'/echo.pid'))
        pod(['sh','-ec','python3 /tmp/echo.py 10.203.73.2 >/tmp/echo.log 2>&1 < /dev/null &'])
        result=remote('python3 -c '+shlex.quote(CLIENT)+' 10.203.73.2')
        report['checks'].append({'name':'vds-to-pod-echo','result':json.loads(result.stdout)})
        result=pod(['python3','-c',CLIENT,'10.203.73.1'])
        report['checks'].append({'name':'pod-to-vds-echo','result':json.loads(result.stdout)})
        phase('Reapply and recover after interface-down')
        vds_command('apply'); pod_command('apply')
        remote('ip link set k0nprobe0 down')
        if vds_command('verify',check=False).returncode==0:
            raise RuntimeError('verify unexpectedly passed while interface was down')
        vds_command('apply'); vds_command('verify'); pod_command('verify')
        report['checks'].append({'name':'repeat-apply-and-interface-down-recovery','passed':True})
        phase('Revoke peer, confirm traffic stops, then restore authorized configuration')
        revoked=json.loads(encoded)
        revoked['nodes'][1]['wireguard']['publicKey']=base64.b64encode(bytes([7])*32).decode()
        put_remote(remote_dir+'/inventory.json',json.dumps(revoked).encode())
        vds_command('apply')
        if vds_command('verify',check=False).returncode==0:
            raise RuntimeError('traffic passed after peer key was revoked')
        put_remote(remote_dir+'/inventory.json',encoded)
        vds_command('apply'); vds_command('verify'); pod_command('verify')
        report['checks'].append({'name':'revocation-blocks-traffic-and-restore-recovers','passed':True})
        report['statusBeforeCleanup']={
            'vds':json.loads(vds_command('status').stdout),
            'pod':json.loads(pod_command('status').stdout)}
        report['passed']=True
        phase('PASS: external transport tests; cleaning up')
    except BaseException as exc:
        report['passed']=False
        report['error']=str(exc)
        phase('FAIL: '+str(exc))
    finally:
        if remote_dir:
            try:
                # Only stop the process started from this unique test directory.
                remote('if test -f '+shlex.quote(remote_dir+'/echo.pid')+'; then '
                       'task_pid=$(cat '+shlex.quote(remote_dir+'/echo.pid')+'); '
                       'if test -r /proc/$task_pid/cmdline && '
                       'tr "\\0" " " < /proc/$task_pid/cmdline | grep -Fq -- '+
                       shlex.quote(remote_dir+'/echo.py')+'; then kill "$task_pid"; fi; fi')
                if applied_vds:
                    vds_command('remove --drained')
                remote('test ! -e /sys/class/net/k0nprobe0')
                remote('rm -rf -- '+shlex.quote(remote_dir))
            except Exception as exc:
                report['cleanupErrors'].append('VDS: '+str(exc))
        if applied_pod:
            try: pod_command('remove --drained')
            except Exception as exc: report['cleanupErrors'].append('Pod transport: '+str(exc))
        if created:
            try: execute(k+['delete','namespace',namespace,'--wait=true','--timeout=60s'],timeout=80)
            except Exception as exc: report['cleanupErrors'].append('namespace: '+str(exc))
        if report['cleanupErrors']: report['passed']=False
        pathlib.Path(args.report).write_text(json.dumps(report,indent=2)+'\n')
        phase('Report: '+args.report)
    return 0 if report.get('passed') else 1


if __name__=='__main__':
    sys.exit(main())
