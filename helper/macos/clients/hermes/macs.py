"""Operator config: which Macs the plugin may address, by name."""
import json
import os
import re

NAME_RE = r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}'
ENV_PATH = 'LINA_HELPER_MACS_FILE'


def config_path(env=None):
    env = os.environ if env is None else env
    return env.get(ENV_PATH) or os.path.join(os.path.dirname(os.path.abspath(__file__)), 'macs.yaml')


def _parse(text):
    """YAML if a YAML library is importable (Hermes ships ruamel.yaml), else JSON in the same file."""
    try:
        from ruamel.yaml import YAML
        return YAML(typ='safe').load(text)
    except ImportError:
        pass
    try:
        import yaml
        return yaml.safe_load(text)
    except ImportError:
        return json.loads(text)


def load(path=None):
    """The parsed config, or None when the file is missing, unreadable or not {"macs": {name: {...}}}."""
    try:
        with open(path or config_path(), encoding='utf-8-sig') as f:
            value = _parse(f.read())
    except Exception:
        return None
    if not isinstance(value, dict) or not isinstance(value.get('macs'), dict):
        return None
    return value


def entry(config, name):
    """The host string for a configured name, or None."""
    if not isinstance(config, dict) or not isinstance(name, str) or not re.fullmatch(NAME_RE, name):
        return None
    macs = config.get('macs')
    item = macs.get(name) if isinstance(macs, dict) else None
    host = item.get('host') if isinstance(item, dict) else None
    return host if isinstance(host, str) and host else None


def names(config):
    macs = config.get('macs') if isinstance(config, dict) else None
    if not isinstance(macs, dict):
        return []
    return [n for n in macs if entry(config, n)]
