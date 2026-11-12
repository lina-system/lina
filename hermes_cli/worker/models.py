"""The main model of a run: chosen per run, defaulting from the kind of work, recorded with the run.

```yaml
worker:
  models:
    aliases:                        # short names for --model
      planner: {provider: opencodex, id: <a strong planning model>, thinking: medium}
      builder: {provider: opencodex, id: <a long-implementation model>, thinking: high}
    defaults:                       # by mode; a mode without an entry uses the runner's own default
      research: planner
      mass-research: planner
      ulw: builder
```

``--model`` takes an alias or ``<provider>/<id>[:<thinking>]`` (the first ``/`` splits, so an id may hold ``/``).
No model is named in code: without config a run keeps the runner's own default and says so.

A model has one identity (lina#385, lina#585): magpie's catalog form ``<provider>/<id>`` (``claude/claude-opus-5-5``),
whichever proxy a node reaches it through (``proxy.py``). It is written in any of these forms:

- OCX's: ``opencodex`` with OCX's namespaced id (``opencodex/anthropic/claude-opus-5-5``), or the namespaced id alone
  (``anthropic/claude-opus-5-5``, OCX's list form);
- magpie's: ``claude/claude-opus-5-5``, or a magpie node's ``<remote_id>/claude/claude-opus-5-5`` (as ``magpie`` with
  that id, or written with the remote id as provider); a node whose own magpie holds the models (``remote_id: ""``,
  lina#623) opens ``magpie`` with the plain ``claude/claude-opus-5-5``.

``opencodex/`` and ``<remote_id>/`` are route markers, not part of the identity. ``canonical`` gives the identity of
every form: it is what the settings check looks up and what the run record and the run page show; older records that
store OCX's names are read through the same table. ``through_proxy`` gives the ``(provider, id)`` the omo runner opens
its session with on a node of each proxy kind. The profile itself keeps the form that was written: a runner that does
not go through a proxy (acp, command) gets it as before.

OCX's names map onto magpie's through one table, ``proxy_names.json`` next to this module (package data; other code
and the omo routing tool read the same file). Its schema is plain JSON with two objects, keys lowercase:

- ``namespaces``: OCX upstream namespace (the part of an OCX namespaced id before its first ``/``) -> magpie provider;
- ``bare_prefixes``: the start of a bare OCX id (OCX's bare OpenAI namespace, no ``/``) -> magpie provider.

The reverse (magpie provider -> OCX form) is the sorted list of OCX forms that map to the provider; when there is more
than one (``antigravity``: ``google`` and ``google-antigravity``) a magpie-form name is refused on an ``ocx`` node with
both candidates named, and the OCX form has to be written instead.
"""

from __future__ import annotations

import functools
import json
from pathlib import Path

from hermes_cli.worker import proxy as proxies
from hermes_cli.worker.runners.base import ModelProfile, RunnerError

THINKING = ("off", "minimal", "low", "medium", "high", "xhigh", "max")
OCX = "opencodex"
MAGPIE = "magpie"
NAMES = Path(__file__).with_name("proxy_names.json")
FAST = "-fast"  # OCX's priority tiers: upstream `x-fast`, published by OCX as `x--fast`; both end with it


@functools.cache
def _table() -> tuple[dict[str, str], dict[str, str]]:
    doc = json.loads(NAMES.read_text(encoding="utf-8-sig"))
    return dict(doc["namespaces"]), dict(doc["bare_prefixes"])


def magpie_providers() -> set[str]:
    namespaces, bare = _table()
    return {*namespaces.values(), *bare.values()}


def ocx_forms(provider: str) -> list[str]:
    """The OCX forms of a magpie provider, sorted: upstream namespaces, ``""`` for OCX's bare namespace."""
    namespaces, bare = _table()
    forms = sorted(ns for ns, to in namespaces.items() if to == provider)
    return forms + ([""] if provider in bare.values() else [])


def _from_ocx(namespaced: str) -> tuple[str, str]:
    namespaces, bare = _table()
    if "/" in namespaced:
        upstream, rest = namespaced.split("/", 1)
        return namespaces.get(upstream, upstream), rest
    found = next((to for prefix, to in bare.items() if namespaced.startswith(prefix)), None)
    return (found, namespaced) if found else (OCX, namespaced)


def canonical(provider: str | None, model_id: str | None) -> tuple[str | None, str | None]:
    """``(provider, id)`` of a model in magpie's catalog form, for every form it may be written in."""
    if not provider or not model_id:
        return provider, model_id
    namespaces, _ = _table()
    if provider == OCX:
        return _from_ocx(model_id)
    if provider in namespaces:
        return namespaces[provider], model_id
    if provider in magpie_providers():
        return provider, model_id
    head, _, rest = model_id.partition("/")
    if provider == MAGPIE and rest and "/" in rest:
        return canonical(*rest.split("/", 1))  # `<remote_id>/<provider>/<id>`
    if rest and head in magpie_providers():
        return head, rest  # written with a magpie node's remote id as provider
    return provider, model_id


def _written(profile: ModelProfile) -> str:
    return f"{profile.provider}/{profile.id}" if profile.provider else str(profile.id)


def _through_ocx(profile: ModelProfile) -> tuple[str | None, str | None]:
    if not profile.provider or profile.provider == OCX:
        return profile.provider, profile.id
    provider, model_id = canonical(profile.provider, profile.id)
    if profile.provider in _table()[0] or provider not in magpie_providers():
        return OCX, f"{profile.provider}/{profile.id}"  # OCX's form (or one the table does not know): as written
    forms = ocx_forms(str(provider))
    if len(forms) != 1:
        raise RunnerError(f"model {_written(profile)}: magpie's {provider!r} is OCX's "
                          f"{' or '.join(repr(f) for f in forms)}; write the OCX form to run it through OCX")
    return OCX, f"{forms[0]}/{model_id}" if forms[0] else str(model_id)


def _through_magpie(profile: ModelProfile, remote_id: str) -> tuple[str, str]:
    provider, model_id = canonical(profile.provider, profile.id)
    if str(model_id).endswith(FAST):
        raise RunnerError(f"model {_written(profile)}: a --fast variant has no equivalent through magpie; "
                          "pick the model without -fast or run it on an ocx node")
    if provider not in magpie_providers():
        raise RunnerError(f"model {_written(profile)}: no magpie provider for {provider!r} in "
                          f"{NAMES.name} (known: {', '.join(sorted(magpie_providers()))})")
    return MAGPIE, f"{remote_id}/{provider}/{model_id}" if remote_id else f"{provider}/{model_id}"


def through_proxy(profile: ModelProfile, proxy: proxies.Proxy) -> tuple[str | None, str | None]:
    """``(provider, id)`` the omo runner opens its session with on a node of *proxy*'s kind: ``opencodex`` and OCX's
    namespaced id, or ``magpie`` and ``<remote_id>/<provider>/<id>`` (``<provider>/<id>`` with an empty remote id). A
    model the proxy cannot serve is refused with ``RunnerError`` naming it; nothing is substituted."""
    if proxy.kind == proxies.MAGPIE:
        return _through_magpie(profile, proxy.remote_id)
    return _through_ocx(profile)


def name(profile: ModelProfile) -> str:
    """``<provider>/<id>[:<thinking>]`` in the canonical form."""
    provider, model_id = canonical(profile.provider, profile.id)
    return f"{provider}/{model_id}" + (f":{profile.thinking}" if profile.thinking else "")


def _aliases(worker: dict) -> dict[str, dict]:
    models = worker.get("models") if isinstance(worker.get("models"), dict) else {}
    aliases = models.get("aliases") if isinstance(models.get("aliases"), dict) else {}
    return {str(k).lower(): v for k, v in aliases.items() if isinstance(v, dict) and v.get("id")}


def _from_alias(alias: str, spec: dict, source: str) -> ModelProfile:
    return ModelProfile(spec.get("provider"), str(spec["id"]), spec.get("thinking"), alias, source)


def parse(value: str, worker: dict, source: str = "--model") -> ModelProfile | None:
    text = (value or "").strip()
    if not text or text.lower() == "default":
        return None
    aliases = _aliases(worker)
    if text.lower() in aliases:
        return _from_alias(text.lower(), aliases[text.lower()], source)
    thinking = None
    if ":" in text and text.rsplit(":", 1)[1] in THINKING:
        text, thinking = text.rsplit(":", 1)
    if "/" not in text:
        raise RunnerError(f"--model: an alias ({', '.join(aliases) or 'none configured'}) or "
                          f"<provider>/<id>[:<thinking>], not {value!r}")
    provider, model_id = text.split("/", 1)
    for alias, spec in aliases.items():
        if (provider, model_id) == (spec.get("provider"), spec.get("id")) and thinking in (None, spec.get("thinking")):
            return _from_alias(alias, spec, source)
    return ModelProfile(provider, model_id, thinking, None, source)


def for_run(mode: str, override: str | None, worker: dict) -> ModelProfile | None:
    """``--model`` when given, else the mode's default from config, else None (the runner's own default)."""
    if override:
        return parse(override, worker)
    models = worker.get("models") if isinstance(worker.get("models"), dict) else {}
    defaults = models.get("defaults") if isinstance(models.get("defaults"), dict) else {}
    name = defaults.get(mode)
    return parse(str(name), worker, source=f"mode:{mode}") if name else None


def label(profile: dict | None) -> str:
    if not profile:
        return "runner default"
    if profile.get("alias"):
        return str(profile["alias"])
    provider, model_id = canonical(profile.get("provider"), profile.get("id"))
    head = f"{provider}/{model_id}" if provider else f"{model_id}"
    return head + (f":{profile['thinking']}" if profile.get("thinking") else "")
