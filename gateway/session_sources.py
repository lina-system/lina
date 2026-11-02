"""Optional session-source capabilities owned by registered platform factories."""
from dataclasses import dataclass
from functools import partial
from typing import Callable


@dataclass(frozen=True)
class SessionSourceFactory:
    """A platform factory carrying its producer's private policy and RPC hooks.

    The platform registry owns discovery, profile scope and registration lifetime.
    No source extension survives removal of its platform registration.
    """

    factory: Callable
    build_policy: Callable
    valid_policy: Callable
    turn_author: Callable
    clarification_error: str
    rpc_methods: dict[str, Callable]

    def __call__(self, config):
        return self.factory(config)


def source_hooks(source):
    from gateway.platform_registry import platform_registry
    from hermes_cli.plugins import discover_plugins
    from gateway.session_policy import SURFACES
    if source in SURFACES:
        return None
    discover_plugins()
    entry = platform_registry.get(source)
    if entry is not None and isinstance(entry.adapter_factory, SessionSourceFactory):
        return entry.adapter_factory
    return None


def policy_hooks(policy):
    hooks = source_hooks(policy.source) if policy is not None else None
    return hooks if hooks is not None and hooks.valid_policy(policy) else None


def rpc_handlers(connection, method):
    """Resolve only the requested RPC namespace, not every deferred platform."""
    hooks = source_hooks(method.partition('.')[0])
    return {name: partial(callback, connection) for name, callback in hooks.rpc_methods.items()} if hooks else {}


def storage_source(db, source, session_id, fallback):
    """Retain local producers' creation labels when refreshing transport metadata."""
    from gateway.config import Platform
    from hermes_state_runtime import RuntimeStoreError
    if source.platform != Platform.LOCAL:
        return fallback
    if not str(source.chat_id).startswith('local-'):
        from hermes_state_local_migration import LEGACY_PREFIX
        with db._read_ctx() as conn:
            if conn.execute('SELECT 1 FROM state_meta WHERE key=?', (LEGACY_PREFIX + str(source.chat_id),)).fetchone() is None:
                return fallback
    from hermes_state_local import local_receipt
    receipt = local_receipt(db, source.chat_id)
    if session_id not in receipt.get('lineage', [receipt['session_id']]):
        raise RuntimeStoreError('admission_conflict')
    label = receipt['policy']['source']
    if 'legacy_session_id' in receipt:
        root = db.get_session(receipt['legacy_session_id'])
        return (root or {}).get('source') or label
    from gateway.session_policy import STORED_AS_LOCAL
    return fallback if label in STORED_AS_LOCAL else label
