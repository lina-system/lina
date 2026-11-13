"""The model proxy of this node (lina#585): the node's local magpie, read from the node's own LINA config.

```yaml
worker:
  proxy:
    remote_id: hub        # the id the node's magpie gives its remote link to the hub (default hub); "" when the
                          # node's own magpie holds the models (the hub itself, lina#623)
    health: http://127.0.0.1:3425/v1/models   # optional: the address the node's proxy check asks (lina#586)
    url: http://127.0.0.1:<port>  # optional; magpie's gateway for the usage, quota and model screens (lina#588);
                                  # default: the origin of `health`, else magpie's default port on 127.0.0.1
    key_env: MAGPIE_KEY   # optional; the name of the variable in `.env` holding a gateway key for those reads
    service: magpie       # the node's own magpie service unit (systemd user unit / launchd label; default magpie)
```

``remote_id`` is the one setting that tells a node that reaches the hub's magpie through a link from the machine
whose own magpie holds the models: magpie's model list does not mark which entries come through a link, and a
model id may itself hold ``/``, so the link cannot be read off the list.

How a node is set up: ``docs/worker-proxy-switch.md``. LINA only reads this section; it never installs or configures
magpie, and never touches its accounts; the device screen's restart (lina#589, ``proxy_service``) restarts only the
node's own local service named here. Keys this section no longer has (``kind``, ``read``) are not read.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

from hermes_cli.worker.runners.base import RunnerError

MAGPIE = "magpie"
_ENV_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
_UNIT = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._@\-]{0,127}$")


@dataclass(frozen=True)
class Proxy:
    remote_id: str = "hub"  # model ids read `<remote_id>/<provider>/<model>`; "": `<provider>/<model>`
    health: str | None = None  # the address device.model_check_url asks instead of the default (lina#586)
    url: str | None = None  # magpie's gateway for the read-only screens (lina#588)
    key_env: str | None = None  # the variable holding the gateway key; the key itself is a secret in `.env`
    service: str = MAGPIE  # the node's local service unit / label the device screen restarts (lina#589)


def of(worker: dict) -> Proxy:
    """The ``worker.proxy`` of a node's ``worker:`` section; a wrong value is refused with the key it is in."""
    section = worker.get("proxy")
    if section is None:
        return Proxy()
    if not isinstance(section, dict):
        raise RunnerError(f"worker.proxy: a mapping with remote_id, health and service, not {section!r}")
    remote_id = section.get("remote_id")
    remote_id = "hub" if remote_id is None else remote_id
    if not isinstance(remote_id, str) or "/" in remote_id:
        raise RunnerError(f"worker.proxy.remote_id: a name without '/' (or \"\" for local), not {remote_id!r}")
    health = section.get("health")
    if health is not None and not isinstance(health, str):
        raise RunnerError(f"worker.proxy.health: a text, not {health!r}")
    url = section.get("url")
    if url is not None and not (isinstance(url, str) and url.startswith(("http://", "https://"))):
        raise RunnerError(f"worker.proxy.url: an http(s) address, not {url!r}")
    key_env = section.get("key_env")
    if key_env is not None and not (isinstance(key_env, str) and _ENV_NAME.fullmatch(key_env)):
        raise RunnerError(f"worker.proxy.key_env: the name of an environment variable, not {key_env!r}")
    service = section.get("service")
    service = MAGPIE if service is None else service
    if not isinstance(service, str) or not _UNIT.match(service):
        raise RunnerError(f"worker.proxy.service: a service unit name (letters, digits, . _ @ -), not {service!r}")
    return Proxy(remote_id, health or None, url or None, key_env, service)
