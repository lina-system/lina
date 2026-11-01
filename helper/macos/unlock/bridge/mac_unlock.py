#!/usr/bin/env python3
"""Bounded SSH adapter for Hermes. No passwords, shell scripts, or auto-install."""
import argparse
import json
import re
import shlex
import subprocess
import sys

CTL='/Library/Application Support/HermesUnlock/bin/hermes-unlockctl'

def command(host,operation):
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}',host):
        raise ValueError('host must be a configured SSH alias or hostname')
    if operation not in ('status','unlock','revoke'):
        raise ValueError('unsupported operation')
    return ['ssh','-T','-o','BatchMode=yes','-o','ConnectTimeout=8','-o','ServerAliveInterval=5',
            '-o','ServerAliveCountMax=2',host,shlex.join(['sudo','-n',CTL,operation])]

def invoke(host,operation,runner=subprocess.run):
    try:
        p=runner(command(host,operation),stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,
                 stderr=subprocess.PIPE,timeout=25,check=False)
    except subprocess.TimeoutExpired:
        return 1,{'schema_version':1,'result':'transport_timeout','desktop_ready':False}
    if len(p.stdout)>16384:
        return 1,{'schema_version':1,'result':'invalid_remote_response','desktop_ready':False}
    try:
        value=json.loads(p.stdout)
        if not isinstance(value,dict) or value.get('schema_version')!=1 or not isinstance(value.get('result'),str):
            raise ValueError('unexpected response')
    except (ValueError,UnicodeError):
        return 1,{'schema_version':1,'result':'ssh_or_sudo_failed' if p.returncode else 'invalid_remote_response',
                  'desktop_ready':False,'remote_exit_code':p.returncode}
    if operation=='unlock':
        numeric_fields=('legacy_keychain_status','data_protection_status','active_displays')
        valid_numbers=all(type(value.get(k)) is int for k in numeric_fields)
        success=(valid_numbers and p.returncode==0 and value.get('desktop_ready') is True and
                 value.get('screen_unlocked') is True and value.get('legacy_keychain_status')==0 and
                 value.get('data_protection_status')==0 and value.get('active_displays',0)>0 and
                 value.get('result') in ('unlocked','already_unlocked') and
                 (value.get('result')=='already_unlocked' or value.get('authorization_consumed') is True))
        if not success: value['desktop_ready']=False
        return 0 if success else 1,value
    return 0 if p.returncode==0 else 1,value

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--host',required=True,help='SSH alias explicitly selected for this Mac')
    p.add_argument('operation',choices=['status','unlock','revoke'])
    args=p.parse_args()
    code,value=invoke(args.host,args.operation)
    print(json.dumps(value,sort_keys=True)); return code

if __name__=='__main__':
    try: raise SystemExit(main())
    except (ValueError,OSError) as e:
        print(json.dumps({'result':'local_error','detail':str(e),'desktop_ready':False}),file=sys.stderr)
        raise SystemExit(1)
