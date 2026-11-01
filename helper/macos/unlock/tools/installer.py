#!/usr/bin/env python3
"""Stage, activate, verify, confirm, and roll back Hermes Unlock on macOS."""
import argparse
import ctypes
import fcntl
import hashlib
import json
import os
from pathlib import Path
import plistlib
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from policy import Policy, Conflict, SCREEN

ROOT = Path('/Library/Application Support/HermesUnlock')
STATE = Path('/var/db/hermes-unlock')
RUNTIME = Path('/var/run/hermes-unlock')
PLUGIN = Path('/Library/Security/SecurityAgentPlugins/HermesUnlockAuthorizationPlugin.bundle')
AGENT = ROOT / 'Hermes Unlock Agent.app'
CTL = ROOT / 'bin/hermes-unlockctl'
LA = Path('/Library/LaunchAgents/local.hermes.unlock.agent.plist')
PREPARE = Path('/Library/LaunchDaemons/local.hermes.unlock.prepare.plist')
WATCHDOG = Path('/Library/LaunchDaemons/local.hermes.unlock.watchdog.plist')
SUDOERS = Path('/etc/sudoers.d/hermes-unlock')
MANIFEST = STATE / 'state.json'
DEVELOPER_ID = 'anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists'


def run(argv, data=None, check=True, timeout=20):
    p = subprocess.run([str(a) for a in argv], input=data, stdout=subprocess.PIPE,
                       stderr=subprocess.PIPE, timeout=timeout, env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','LANG':'C'})
    if check and p.returncode:
        # No credentials or arbitrary command output are included in diagnostics.
        raise RuntimeError('command failed: ' + str(argv[0]) + ' (exit ' + str(p.returncode) + ')')
    return p


def protected_directory(path):
    path.mkdir(mode=0o700, parents=False, exist_ok=True)
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    st = os.fstat(fd)
    if st.st_uid != 0 or stat.S_IMODE(st.st_mode) != 0o700:
        os.close(fd)
        raise Conflict('state directory must be root-owned with mode 0700')
    return fd


def atomic_json(path, value):
    fd, temporary = tempfile.mkstemp(prefix=path.name+'.',suffix='.new',dir=path.parent)
    temp = Path(temporary)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(value, f, sort_keys=True, indent=2)
            f.write('\n'); f.flush(); os.fsync(f.fileno())
        os.replace(temp, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try: os.fsync(directory)
        finally: os.close(directory)
    finally:
        if temp.exists(): temp.unlink()


class Backend:
    def load(self):
        fd=os.open(MANIFEST, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd) as f:
            st=os.fstat(f.fileno())
            if not stat.S_ISREG(st.st_mode) or st.st_uid != 0 or stat.S_IMODE(st.st_mode) != 0o600 or st.st_nlink != 1:
                raise Conflict('unsafe state file')
            return json.load(f)
    def save(self, value): atomic_json(MANIFEST,value)
    def now(self):
        # Unlike mach_absolute_time, this deadline includes time spent asleep.
        lib=ctypes.CDLL('/usr/lib/libSystem.B.dylib')
        lib.mach_continuous_time.restype=ctypes.c_uint64
        scale=(ctypes.c_uint32*2)()
        if lib.mach_timebase_info(ctypes.byref(scale)) or not scale[1]:
            raise Conflict('continuous clock unavailable')
        return lib.mach_continuous_time()*scale[0]/scale[1]/1_000_000_000
    def boot(self): return run(['/usr/sbin/sysctl','-n','kern.bootsessionuuid']).stdout.decode().strip()
    def read_right(self,name):
        p=run(['/usr/bin/security','authorizationdb','read',name],check=False)
        if p.returncode:
            if b'-60005' in p.stderr: return None
            raise Conflict('cannot read authorization right: '+name)
        return plistlib.loads(p.stdout)
    def write_right(self,name,value):
        run(['/usr/bin/security','authorizationdb','write',name],plistlib.dumps(value))
    def remove_right(self,name): run(['/usr/bin/security','authorizationdb','remove',name])
    def status(self): return json.loads(run([CTL,'status']).stdout)
    def unlock(self):
        p=run([CTL,'unlock'],check=False,timeout=25)
        value=json.loads(p.stdout)
        if p.returncode: raise Conflict('native unlock failed: '+str(value.get('result','unknown')))
        return value
    def preflight(self):
        run(['/bin/launchctl','print','system/local.hermes.unlock.watchdog'])
        state=self.status()
        if not state.get('installed') or not state.get('console_known') or state.get('screen_locked') is not False:
            raise Conflict('preflight requires the configured GUI user already unlocked')
        if not state.get('agent_connected') or not state.get('accessibility') or state.get('active_displays',0)<1:
            raise Conflict('GUI agent, Accessibility permission and active display required')
        if not json.loads(run([CTL,'health']).stdout).get('desktop_ready'):
            raise Conflict('both keychain probes must pass before activation')


def verify_artifacts(build, lab):
    artifacts=[build/'hermes-unlockctl',build/'Hermes Unlock Agent.app',build/'HermesUnlockAuthorizationPlugin.bundle']
    for item in artifacts:
        if not item.exists() or item.is_symlink(): raise Conflict('missing or linked build artifact: '+item.name)
        if item.is_dir() and any(p.is_symlink() for p in item.rglob('*')):
            raise Conflict('symlinks are not accepted in the build payload')
        args=['/usr/bin/codesign','--verify','--strict']
        if not lab: args += ['-R','='+DEVELOPER_ID]
        run(args+[item])
    return artifacts


def write_plist(path,value):
    with path.open('xb') as f: f.write(plistlib.dumps(value)); f.flush(); os.fsync(f.fileno())
    os.chmod(path,0o644)


def lock_down(path):
    for p in [path]+list(path.rglob('*')):
        if p.is_symlink(): raise Conflict('symlink in installation payload')
        os.chown(p,0,0)
        mode=0o755 if p.is_dir() or p.stat().st_mode & 0o111 else 0o644
        os.chmod(p,mode)


def trusted_python(candidates=None):
    candidates=candidates or [Path('/usr/bin/python3'),Path(sys.executable)]
    for candidate in candidates:
        try:
            path=candidate.resolve(strict=True)
            chain=[path]+list(path.parents)
            if not path.is_file() or not os.access(path,os.X_OK): continue
            if all(p.stat().st_uid==0 and not p.stat().st_mode & 0o022 for p in chain):
                return path
        except OSError:
            continue
    raise Conflict('watchdog needs a root-owned Python 3 interpreter and non-writable parent directories')


def service_unload(uid):
    for domain,label in [('gui/'+str(uid),'local.hermes.unlock.agent'),('system','local.hermes.unlock.prepare'),('system','local.hermes.unlock.watchdog')]:
        run(['/bin/launchctl','bootout',domain+'/'+label],check=False)


def remove_owned_files(state):
    service_unload(state['uid'])
    # Top-level paths were exclusively created by stage(); no unrelated paths
    # are accepted from the manifest or from command-line arguments.
    targets=[LA,PREPARE,WATCHDOG,PLUGIN,ROOT]
    if state.get('sudoers'): targets.append(SUDOERS)
    for p in targets:
        if p.is_symlink(): raise Conflict('installed path became a symlink: '+str(p))
        if p.is_dir(): shutil.rmtree(p)
        elif p.exists(): p.unlink()
    runtime=RUNTIME
    if runtime.exists():
        st=runtime.lstat()
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0: raise Conflict('unsafe runtime directory')
        shutil.rmtree(runtime)


def stage(args,backend):
    if not re.fullmatch(r'[a-z_][a-z0-9_-]*',args.user or ''): raise Conflict('provide a local short username')
    user=pwd.getpwnam(args.user)
    if user.pw_uid<501: raise Conflict('choose a non-system GUI user')
    if MANIFEST.exists() and backend.load().get('phase') != 'uninstalled':
        raise Conflict('package already staged; uninstall before replacing binaries')
    build=args.build.resolve(); verify_artifacts(build,args.allow_ad_hoc_for_lab)
    for p in [ROOT,PLUGIN,LA,PREPARE,WATCHDOG]+([SUDOERS] if args.enable_ssh_command else []):
        if p.exists() or p.is_symlink(): raise Conflict('existing installation path: '+str(p))
    python=trusted_python()
    run(['/bin/launchctl','print','gui/'+str(user.pw_uid)])
    state={'format':1,'phase':'installing','uid':user.pw_uid,'user':args.user,
           'sudoers':args.enable_ssh_command,'lab_signature':args.allow_ad_hoc_for_lab,
           'version':'0.2.0','deadline':None}
    backend.save(state)
    try:
        ROOT.mkdir(mode=0o755)
        (ROOT/'bin').mkdir(); (ROOT/'tools').mkdir()
        shutil.copy2(build/'hermes-unlockctl',CTL)
        shutil.copytree(build/'Hermes Unlock Agent.app',AGENT)
        shutil.copytree(build/'HermesUnlockAuthorizationPlugin.bundle',PLUGIN)
        for name in ['installer.py','policy.py']:
            shutil.copy2(Path(__file__).parent/name,ROOT/'tools'/name)
        (ROOT/'allowed-uid').write_text(str(user.pw_uid)+'\n')
        lock_down(ROOT); lock_down(PLUGIN)
        # Re-check the actual installed bytes before launch or policy activation.
        for item in [CTL,AGENT,PLUGIN]:
            verification=['/usr/bin/codesign','--verify','--strict']
            if not args.allow_ad_hoc_for_lab: verification+=['-R','='+DEVELOPER_ID]
            run(verification+[item])
        run([CTL,'prepare-runtime'])
        write_plist(LA,{'Label':'local.hermes.unlock.agent','ProgramArguments':[str(AGENT/'Contents/MacOS/hermes-unlock-agent'),'serve'],
                       'RunAtLoad':True,'KeepAlive':{'SuccessfulExit':False},'ThrottleInterval':10,'LimitLoadToSessionType':'Aqua'})
        write_plist(PREPARE,{'Label':'local.hermes.unlock.prepare','ProgramArguments':[str(CTL),'prepare-runtime'],'RunAtLoad':True})
        write_plist(WATCHDOG,{'Label':'local.hermes.unlock.watchdog','ProgramArguments':[str(python),str(ROOT/'tools/installer.py'),'watchdog'],
                             'RunAtLoad':True,'StartInterval':15})
        if args.enable_ssh_command:
            digest=hashlib.sha256(CTL.read_bytes()).hexdigest()
            command=str(CTL).replace(' ','\\ ')
            entries=', '.join('sha256:'+digest+' '+command+' '+op for op in ['status','unlock','revoke'])
            text=args.user+' ALL=(root) NOPASSWD: '+entries+'\n'
            pending=STATE/'sudoers.pending'
            pending.write_text(text); os.chmod(pending,0o600)
            try:
                run(['/usr/sbin/visudo','-cf',pending])
                with SUDOERS.open('x') as f: f.write(text)
                os.chmod(SUDOERS,0o440)
            finally: pending.unlink(missing_ok=True)
        state['phase']='staged'; backend.save(state)
        run(['/bin/launchctl','bootstrap','system',PREPARE])
        run(['/bin/launchctl','bootstrap','system',WATCHDOG])
        run(['/bin/launchctl','bootstrap','gui/'+str(user.pw_uid),LA])
        return state
    except BaseException:
        remove_owned_files(state)
        state['phase']='uninstalled'; backend.save(state)
        raise


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=['plan','install','status','activate','verify-unlock','confirm','rollback','watchdog','uninstall'])
    parser.add_argument('--build',type=Path,default=Path(__file__).resolve().parents[1]/'build')
    parser.add_argument('--user')
    parser.add_argument('--enable-ssh-command',action='store_true')
    parser.add_argument('--allow-ad-hoc-for-lab',action='store_true')
    parser.add_argument('--rollback-after',type=int,default=120)
    args=parser.parse_args()
    if args.command=='plan':
        print(json.dumps({'mutates_system':False,'stage_paths':[str(p) for p in [ROOT,PLUGIN,LA,PREPARE,WATCHDOG]],
            'activation':'separate; automatic rollback until verified and confirmed',
            'signing':'Developer ID by default; explicit ad-hoc lab override',
            'remote_command':'SSH plus optional exact-command sudoers entries'},indent=2)); return 0
    if sys.platform!='darwin' or os.geteuid()!=0:
        raise Conflict('system operations require root on macOS; use plan for read-only preparation')
    fd=protected_directory(STATE)
    try:
        lock=os.open('installer.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600,dir_fd=fd)
        try:
            st=os.fstat(lock)
            if st.st_uid!=0 or st.st_nlink!=1 or stat.S_IMODE(st.st_mode)!=0o600: raise Conflict('unsafe installer lock')
            fcntl.flock(lock,fcntl.LOCK_EX)
            backend=Backend(); policy=Policy(backend)
            if args.command=='install': result=stage(args,backend)
            elif args.command=='status': result={'installation':backend.load(),'native':backend.status()}
            elif args.command=='activate': result=policy.activate(args.rollback_after)
            elif args.command=='verify-unlock': result=policy.verify()
            elif args.command=='confirm': result=policy.confirm()
            elif args.command=='rollback': result=policy.rollback()
            elif args.command=='watchdog':
                state=backend.load()
                if state.get('phase') not in ('activating','trial'): return 0
                previous=state['phase']; result=policy.expire()
                if result['phase']==previous: return 0
            else:
                state=backend.load()
                if state.get('phase')=='installing':
                    # Interrupted staging never touches the authorization DB.
                    result=state
                elif state.get('phase')=='uninstalled': return 0
                else: result=policy.rollback()
                if CTL.exists(): run([CTL,'revoke'],check=False)
                remove_owned_files(result)
                result['phase']='uninstalled'; backend.save(result)
            print(json.dumps(result,sort_keys=True,indent=2)); return 0
        finally: os.close(lock)
    finally: os.close(fd)

if __name__=='__main__':
    try: raise SystemExit(main())
    except (Conflict,RuntimeError,OSError,ValueError,subprocess.TimeoutExpired) as e:
        print(json.dumps({'error':str(e),'automatic_approval_requested':False}),file=sys.stderr)
        raise SystemExit(1)
