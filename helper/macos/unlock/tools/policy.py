"""Transactional authorization rule management. No OS calls in this module."""
import copy

SCREEN = 'system.login.screensaver'
REMOTE = 'local.hermes.unlock.remote'
ORIGINAL = 'local.hermes.unlock.original'
REMOTE_RULE = {'class': 'evaluate-mechanisms', 'mechanisms': ['HermesUnlockAuthorizationPlugin:authorize,privileged'], 'shared': False, 'tries': 1}
WRAPPER = {'class': 'rule', 'rule': [REMOTE, ORIGINAL], 'k-of-n': 1, 'shared': False, 'tries': 1}

class Conflict(RuntimeError):
    pass

def normalized(rule):
    if rule is None:
        return None
    return {k: copy.deepcopy(v) for k, v in rule.items() if k not in ('created', 'modified', 'version')}

def reserved_reference(value):
    if isinstance(value, dict): return any(reserved_reference(v) for v in value.values())
    if isinstance(value, list): return any(reserved_reference(v) for v in value)
    return value in (REMOTE, ORIGINAL) if isinstance(value, str) else False

class Policy:
    """Backend state must be persisted before every authorization DB mutation.

    A separate OS file lock serializes installer and watchdog processes.
    Production saves are atomic, root-owned and fsynced.
    """
    def __init__(self, backend):
        self.b = backend

    def _write(self, name, value):
        self.b.write_right(name, value)
        if normalized(self.b.read_right(name)) != normalized(value):
            raise Conflict('authorization read-back mismatch: ' + name)

    def activate(self, seconds=120):
        if not 30 <= seconds <= 600:
            raise Conflict('rollback timeout must be between 30 and 600 seconds')
        state = self.b.load()
        if state.get('phase') != 'staged':
            raise Conflict('stage the package before activation')
        if self.b.read_right(REMOTE) is not None or self.b.read_right(ORIGINAL) is not None:
            raise Conflict('reserved authorization right already exists')
        original = self.b.read_right(SCREEN)
        if not original:
            raise Conflict('screen authorization rule unavailable')
        if reserved_reference(original):
            raise Conflict('existing screen rule refers to reserved names')
        self.b.preflight()
        state.update(phase='activating', original=original, expected=copy.deepcopy(WRAPPER),
                     deadline=self.b.now()+seconds, boot=self.b.boot(), verification=None)
        self.b.save(state)
        try:
            self._write(ORIGINAL, original)
            self._write(REMOTE, REMOTE_RULE)
            self._write(SCREEN, WRAPPER)
            state['phase'] = 'trial'
            self.b.save(state)
        except BaseException:
            self.rollback()
            raise
        return state

    def rollback(self):
        state = self.b.load()
        if state.get('phase') == 'staged':
            return state
        if state.get('phase') not in ('activating', 'trial', 'confirmed'):
            raise Conflict('unknown state; refusing to alter authorization')
        live = normalized(self.b.read_right(SCREEN))
        original = state['original']
        if live == normalized(state['expected']):
            self._write(SCREEN, original)
        elif live != normalized(original):
            raise Conflict('screen rule changed externally; preserve files and resolve manually')
        for name, expected in [(REMOTE, REMOTE_RULE), (ORIGINAL, original)]:
            value = self.b.read_right(name)
            if value is not None:
                if normalized(value) != normalized(expected):
                    raise Conflict('reserved rule changed externally: ' + name)
                self.b.remove_right(name)
                if self.b.read_right(name) is not None:
                    raise Conflict('authorization right removal failed: ' + name)
        state.update(phase='staged', deadline=None, verification=None)
        self.b.save(state)
        return state

    def expire(self):
        state = self.b.load()
        if state.get('phase') in ('activating', 'trial') and (
                state.get('boot') != self.b.boot() or self.b.now() >= state['deadline']):
            return self.rollback()
        return state

    def verify(self):
        state = self.expire()
        if state.get('phase') != 'trial':
            raise Conflict('verification requires a live activation trial')
        before = self.b.status()
        if before.get('screen_locked') is not True:
            raise Conflict('verification must begin with the target session locked')
        result = self.b.unlock()
        required = {'result': 'unlocked', 'desktop_ready': True, 'screen_unlocked': True,
                    'authorization_consumed': True, 'legacy_keychain_status': 0,
                    'data_protection_status': 0}
        if any(result.get(k) != v for k, v in required.items()):
            raise Conflict('live unlock or keychain verification failed')
        state = self.expire()
        if state.get('phase') != 'trial':
            raise Conflict('trial expired during verification')
        state['verification'] = {'at': self.b.now(), 'result': result}
        self.b.save(state)
        return result

    def confirm(self):
        state = self.expire()
        if state.get('phase') != 'trial' or not state.get('verification'):
            raise Conflict('confirm requires verified locked-to-unlocked transition and both keychains')
        if normalized(self.b.read_right(SCREEN)) != normalized(WRAPPER):
            raise Conflict('screen rule changed before confirmation')
        self.b.preflight()
        state = self.expire()
        if state.get('phase') != 'trial':
            raise Conflict('trial expired during confirmation preflight')
        state.update(phase='confirmed', deadline=None)
        self.b.save(state)
        return state
