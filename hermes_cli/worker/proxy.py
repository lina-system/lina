"""The model proxy of this node (lina#585): the proxy its runners reach models through, read from the node's own
LINA config. Absent means ``ocx``, the node's own OCX, as before.

```yaml
worker:
  proxy:
    kind: magpie          # ocx (default) or magpie: the node's local magpie, installed separately
    remote_id: hub        # the id the node's magpie gives its remote link to the hub (default hub)
    health: http://127.0.0.1:3425/v1/models   # optional: the address the node's proxy check asks (lina#586)
    url: http://127.0.0.1:<port>  # optional; magpie's gateway for the usage, quota and model screens (lina#588);
                                  # default: the origin of `health`, else magpie's default port on 127.0.0.1
    key_env: MAGPIE_KEY   # optional; the name of the variable in `.env` holding a gateway key for those reads
    read: [ocx, magpie]   # optional; the proxies those screens read, side by side (default: `kind` alone)
```

How a node is moved to magpie and back: ``docs/worker-proxy-switch.md``. LINA only reads this section; it never
installs, configures or restarts magpie. Model names per kind: ``models.through_proxy``.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

from hermes_cli.worker.runners.base import RunnerError

OCX = "ocx"
MAGPIE = "magpie"
KINDS = (OCX, MAGPIE)
_ENV_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")


@dataclass(frozen=True)
class Proxy:
    kind: str = OCX
    remote_id: str = "hub"  # on a magpie node, model ids read `<remote_id>/<provider>/<model>`
    health: str | None = None  # the address device.model_check_url asks instead of the kind's default (lina#586)
    url: str | None = None  # magpie's gateway for the read-only screens (lina#588)
    key_env: str | None = None  # the variable holding the gateway key; the key itself is a secret in `.env`
    read: tuple[str, ...] = ()  # the proxies the screens read; empty means `kind` alone

    def reads(self) -> tuple[str, ...]:
        """The proxies the usage, quota and model screens read, in order (lina#588)."""
        return self.read or (self.kind,)


def of(worker: dict) -> Proxy:
    """The ``worker.proxy`` of a node's ``worker:`` section; a wrong value is refused with the key it is in."""
    section = worker.get("proxy")
    if section is None:
        return Proxy()
    if not isinstance(section, dict):
        raise RunnerError(f"worker.proxy: a mapping with kind, remote_id and health, not {section!r}")
    kind = section.get("kind")
    kind = OCX if kind is None else kind
    if kind not in KINDS:
        raise RunnerError(f"worker.proxy.kind: one of {', '.join(KINDS)}, not {kind!r}")
    remote_id = section.get("remote_id")
    remote_id = "hub" if remote_id is None else remote_id
    if not isinstance(remote_id, str) or not remote_id or "/" in remote_id:
        raise RunnerError(f"worker.proxy.remote_id: a name without '/', not {remote_id!r}")
    health = section.get("health")
    if health is not None and not isinstance(health, str):
        raise RunnerError(f"worker.proxy.health: a text, not {health!r}")
    url = section.get("url")
    if url is not None and not (isinstance(url, str) and url.startswith(("http://", "https://"))):
        raise RunnerError(f"worker.proxy.url: an http(s) address, not {url!r}")
    key_env = section.get("key_env")
    if key_env is not None and not (isinstance(key_env, str) and _ENV_NAME.fullmatch(key_env)):
        raise RunnerError(f"worker.proxy.key_env: the name of an environment variable, not {key_env!r}")
    return Proxy(kind, remote_id, health or None, url or None, key_env, _read(section.get("read")))


def _read(value) -> tuple[str, ...]:
    if value is None:
        return ()
    if not isinstance(value, list) or not value or any(kind not in KINDS for kind in value) or len(set(value)) < len(value):
        raise RunnerError(f"worker.proxy.read: a list of {', '.join(KINDS)} without repeats, not {value!r}")
    return tuple(value)
