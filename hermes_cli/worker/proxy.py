"""The model proxy of this node (lina#585): the proxy its runners reach models through, read from the node's own
LINA config. Absent means ``ocx``, the node's own OCX, as before.

```yaml
worker:
  proxy:
    kind: magpie          # ocx (default) or magpie: the node's local magpie, installed separately
    remote_id: hub        # the id the node's magpie gives its remote link to the hub (default hub)
    health: http://127.0.0.1:<port>/healthz   # optional; kept for the health report (lina#586)
```

How a node is moved to magpie and back: ``docs/worker-proxy-switch.md``. LINA only reads this section; it never
installs, configures or restarts magpie. Model names per kind: ``models.through_proxy``.
"""

from __future__ import annotations

from dataclasses import dataclass

from hermes_cli.worker.runners.base import RunnerError

OCX = "ocx"
MAGPIE = "magpie"
KINDS = (OCX, MAGPIE)


@dataclass(frozen=True)
class Proxy:
    kind: str = OCX
    remote_id: str = "hub"  # on a magpie node, model ids read `<remote_id>/<provider>/<model>`
    health: str | None = None  # read and kept, used by the health report (lina#586)


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
    return Proxy(kind, remote_id, health or None)
