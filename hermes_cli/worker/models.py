"""The main model of a run: chosen per run, defaulting from the kind of work, recorded with the run.

```yaml
worker:
  models:
    aliases:                        # short names for --model
      planner: {provider: claude, id: <a strong planning model>, thinking: medium}
      builder: {provider: codex, id: <a long-implementation model>, thinking: high}
    defaults:                       # by mode; a mode without an entry uses the runner's own default
      research: planner
      mass-research: planner
      ulw: builder
```

``--model`` takes an alias or ``<provider>/<id>[:<thinking>]`` (the first ``/`` splits, so an id may hold ``/``).
No model is named in code: without config a run keeps the runner's own default and says so.

A model is named in magpie's catalog form ``<provider>/<id>`` (``claude/claude-opus-5-5``), the name the run record,
the settings check and the run page show. ``through_proxy`` gives the ``(provider, id)`` the omo runner opens its
session with through the node's magpie (``proxy.py``): ``magpie`` and ``<remote_id>/<provider>/<id>``, or
``<provider>/<id>`` on a node whose own magpie holds the models (``remote_id: ""``, lina#623). The profile itself
keeps the form that was written: a runner that does not go through the proxy (acp, command) gets it as before.
"""

from __future__ import annotations

from hermes_cli.worker import proxy as proxies
from hermes_cli.worker.runners.base import ModelProfile, RunnerError

THINKING = ("off", "minimal", "low", "medium", "high", "xhigh", "max")
MAGPIE = "magpie"


def _written(profile: ModelProfile) -> str:
    return f"{profile.provider}/{profile.id}" if profile.provider else str(profile.id)


def through_proxy(profile: ModelProfile, proxy: proxies.Proxy) -> tuple[str, str]:
    """``(provider, id)`` the omo runner opens its session with: ``magpie`` and ``<remote_id>/<provider>/<id>``
    (``<provider>/<id>`` with an empty remote id). A model written without its provider is refused with
    ``RunnerError``; nothing is substituted."""
    if not profile.provider:
        raise RunnerError(f"model {_written(profile)}: write it as <provider>/<id>, magpie's catalog form")
    model = f"{profile.provider}/{profile.id}"
    return MAGPIE, f"{proxy.remote_id}/{model}" if proxy.remote_id else model


def name(profile: ModelProfile) -> str:
    """``<provider>/<id>[:<thinking>]``."""
    return _written(profile) + (f":{profile.thinking}" if profile.thinking else "")


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
    provider, model_id = profile.get("provider"), profile.get("id")
    head = f"{provider}/{model_id}" if provider else f"{model_id}"
    return head + (f":{profile['thinking']}" if profile.get("thinking") else "")
