"""What a ``git push`` would send, read from the repository (lina#308, ``personal.py``).

A push's words do not say what it sends: a plain ``git push origin main`` sends every commit the remote does not have
yet. ``outgoing`` finds those commits the way git knows them, reachable from what is pushed and from no
remote-tracking ref of the destination remote, and answers the paths they add or change and the lines they add.
What is pushed is git's choice too: the refspecs and ``--all``/``--branches``/``--mirror``/``--tags``/
``--follow-tags`` (or ``push.followTags``) the command names, else the remote's ``remote.<name>.push`` refspecs, else what ``push.default`` sends (the current branch, or
every local branch for ``matching``). ``personal.py`` judges them. A push whose outgoing commits cannot be told (no
remote-tracking ref of the remote, a remote given as a URL, a pattern refspec, a ``push.default`` it does not know, a
git error) raises ``Undetermined``.
"""

from __future__ import annotations

import os
import subprocess
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path

_TIMEOUT = 30
_VALUE_OPTIONS = {"-o", "--push-option", "--repo", "--receive-pack", "--exec"}
_RANGE_FLAGS = {"--all": "--branches", "--branches": "--branches", "--mirror": "--all", "--tags": "--tags"}
# What a push that names no refspec and whose remote has no push refspec sends, by ``push.default`` (git's default
# is ``simple``): nothing, the current branch, or every branch the remote also has (here: every local branch).
_PUSH_DEFAULT = {"nothing": [], "current": ["HEAD"], "simple": ["HEAD"], "upstream": ["HEAD"],
                 "tracking": ["HEAD"], "matching": ["--branches"]}
# Each outgoing commit's own change: a merge against its first parent, a rename as a new path.
_LOG = ("log", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--diff-merges=first-parent")


class Undetermined(Exception):
    pass


@dataclass(frozen=True)
class Outgoing:
    paths: list[str]
    added: list[str]


@dataclass(frozen=True)
class _Push:
    cwd: str
    global_args: list[str]
    remote: str
    refspecs: list[str]
    ranges: list[str]
    follow_tags: bool | None = None  # ``--follow-tags`` / ``--no-follow-tags`` as written, last one wins


def _git(push: _Push, *args: str) -> str:
    from hermes_cli._subprocess_compat import noninteractive_git_env, windows_hide_flags
    try:
        done = subprocess.run(["git", *push.global_args, *args], cwd=push.cwd, capture_output=True, check=False,
                              timeout=_TIMEOUT, stdin=subprocess.DEVNULL, env=noninteractive_git_env(),
                              creationflags=windows_hide_flags())
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise Undetermined(f"git {args[0]} did not run: {exc}") from exc
    if done.returncode != 0:
        detail = done.stderr.decode("utf-8", "replace").strip().splitlines()
        raise Undetermined(detail[0] if detail else f"git {args[0]} failed")
    return done.stdout.decode("utf-8", "replace")


def _global_args(words: list[str]) -> tuple[list[str], int]:
    """git's options before ``push`` that choose the repository (``-C``, ``--git-dir``, ``--work-tree``), and where
    ``push`` is, and the ``-c`` settings that choose what is pushed (``push.default``, ``remote.<name>.push``). Other
    ``-c`` settings are not carried: the check must not run configuration from the command."""
    kept, index = [], 1
    while index < len(words) and words[index] != "push":
        word = words[index]
        if word in {"-C", "--git-dir", "--work-tree"} and index + 1 < len(words):
            kept += [word, words[index + 1]]
            index += 1
        elif word.startswith(("--git-dir=", "--work-tree=")):
            kept.append(word)
        elif word == "-c":
            setting = words[index + 1] if index + 1 < len(words) else ""
            if _push_setting(setting.split("=", 1)[0]):
                kept += [word, setting]
            index += 1
        index += 1
    return kept, index


def _parse(words: list[str], cwd: str) -> _Push:
    global_args, at = _global_args(words)
    positional, ranges, remote, follow_tags = [], [], "", None
    rest = iter(words[at + 1:])
    for word in rest:
        if word in _VALUE_OPTIONS:
            value = next(rest, "")
            remote = value if word == "--repo" else remote
        elif word.startswith("--repo="):
            remote = word.split("=", 1)[1]
        elif word in _RANGE_FLAGS:
            ranges.append(_RANGE_FLAGS[word])
        elif word in {"--follow-tags", "--no-follow-tags"}:
            follow_tags = word == "--follow-tags"
        elif not word.startswith("-"):
            positional.append(word)
    if positional and not remote:
        remote = positional.pop(0)
    return _Push(cwd, global_args, remote, positional, ranges, follow_tags)


def _default_remote(push: _Push) -> str:
    """The remote a push with no repository goes to: the branch's push remote, the default one, the branch's own
    remote, else ``origin`` (git's order)."""
    branch = _git(push, "symbolic-ref", "--quiet", "--short", "HEAD").strip()
    for key in (f"branch.{branch}.pushRemote", "remote.pushDefault", f"branch.{branch}.remote"):
        found = _git(push, "config", "--default", "", "--get", key).strip()
        if found:
            return found
    return "origin"


def _push_setting(key: str) -> bool:
    parts = key.split(".")
    return key.lower() in {"push.default", "push.followtags"} or (
        len(parts) >= 3 and parts[0].lower() == "remote" and parts[-1].lower() in {"push", "mirror"})


def _is_true(value: str) -> bool:
    """git's boolean: true, yes, on, or a non-zero number."""
    value = value.strip().lower()
    return value in {"true", "yes", "on"} or (value.lstrip("-").isdigit() and int(value) != 0)


def _configured_sources(push: _Push, remote: str) -> list[str]:
    """What git pushes when the command names no refspec: every ref when the remote is a mirror
    (``remote.<name>.mirror``), else the remote's ``remote.<name>.push`` refspecs, else what ``push.default`` sends."""
    settings = []
    for entry in _git(push, "config", "-z", "--list").split("\0"):
        key, has_value, value = entry.partition("\n")
        if key:
            settings.append((key, value if has_value else "true"))  # a key with no value is a true boolean
    if _is_true(next((value for key, value in reversed(settings) if key == f"remote.{remote}.mirror"), "")):
        return ["--all"]
    refspecs = [value for key, value in settings if key == f"remote.{remote}.push"]
    if refspecs:
        return _refspec_sources(refspecs)
    mode = next((value for key, value in reversed(settings) if key == "push.default"), "simple")
    if mode not in _PUSH_DEFAULT:
        raise Undetermined(f"push.default {mode} is not one the check knows")
    return list(_PUSH_DEFAULT[mode])


def _refspec_sources(refspecs: list[str]) -> list[str]:
    sources = []
    specs = iter(refspecs)
    for spec in specs:
        if spec == "tag":
            spec = f"refs/tags/{next(specs, '')}"
        if spec.lstrip("+") == ":":
            sources.append("--branches")  # every branch the remote already has, as push.default=matching
            continue
        source = spec.lstrip("+").split(":", 1)[0]
        if "*" in source:
            raise Undetermined(f"the refspec {spec} is a pattern")
        if source:
            sources.append(source)
    return sources


def _sources(push: _Push, remote: str) -> list[str]:
    if push.ranges or push.refspecs:
        return [*push.ranges, *_refspec_sources(push.refspecs)]
    return _configured_sources(push, remote)


def _outgoing(push: _Push) -> Outgoing:
    remote = push.remote or _default_remote(push)
    if not _git(push, "for-each-ref", "--count=1", "--format=%(refname)", f"refs/remotes/{remote}/").strip():
        raise Undetermined(f"no remote-tracking ref of {remote}")
    sources = _sources(push, remote)
    if not sources:
        return Outgoing([], [])
    span = [*sources, "--not", f"--remotes={remote}", "--"]
    top = Path(_git(push, "rev-parse", "--show-toplevel").strip())
    names = _git(push, *_LOG, "--format=", "--name-only", "--diff-filter=d", "-z", *span)
    added = _git(push, *_LOG, "--format=", "-p", "--unified=0", *span)
    # What a push publishes is the files it changes and the messages that travel with them: each outgoing commit's
    # message and the message of each annotated tag it names.
    messages = _git(push, *_LOG, "--format=%B", *span)
    return Outgoing(list(dict.fromkeys(str(top / name) for name in names.split("\0") if name.strip())),
                    [*(line[1:] for line in added.splitlines() if line.startswith("+") and not line.startswith("+++")),
                     *messages.splitlines(), *_tag_messages(push, sources)])


def _follows_tags(push: _Push) -> bool:
    """``--follow-tags`` as written, else ``push.followTags``: git then also sends the annotated tags that point at
    commits the pushed refs reach."""
    if push.follow_tags is not None:
        return push.follow_tags
    return _is_true(_git(push, "config", "--default", "false", "--get", "push.followTags"))


def _tag_names(push: _Push, sources: list[str]) -> list[str]:
    """The tags a push can send beyond the ones it names: every tag for ``--tags`` and for a push of every ref
    (``--mirror``, a mirror remote), and with ``--follow-tags`` each tag at a commit the pushed refs reach (the
    remote's own tags are not known, so a tag it already has is read too)."""
    if "--tags" in sources or "--all" in sources:
        return _git(push, "for-each-ref", "--format=%(refname)", "refs/tags/").split()
    if not _follows_tags(push):
        return []
    refs = [source for source in sources if not source.startswith("-") and ".." not in source]
    if "--branches" in sources:
        refs += _git(push, "for-each-ref", "--format=%(refname)", "refs/heads/").split()
    names: list[str] = []
    for ref in refs:
        names += _git(push, "for-each-ref", "--format=%(refname)", f"--merged={ref}", "refs/tags/").split()
    return names


def _tag_messages(push: _Push, sources: list[str]) -> list[str]:
    """The lines of each annotated tag the push sends (``git push origin v1``, ``--tags``, ``--follow-tags``): a tag
    object has a message of its own, which no commit of the pushed range holds."""
    named = [source for source in sources if not source.startswith("-") and ".." not in source]
    lines: list[str] = []
    for source in dict.fromkeys([*_tag_names(push, sources), *named]):
        if _git(push, "cat-file", "-t", source).strip() == "tag":
            lines.extend(_git(push, "cat-file", "tag", source).splitlines())
    return lines


def in_directory(commands: list[list[str]], cwd: str) -> Iterator[tuple[list[str], str]]:
    """Each simple command of a shell line with the directory it runs in, following ``cd`` and ``pushd`` (options
    skipped) from *cwd*. A ``cd`` with no usable target (``cd``, ``cd -``, a folder that does not exist) leaves the directory as it was."""
    for words in commands:
        yield words, cwd
        if Path(words[0]).name in {"cd", "pushd"}:
            targets = [word for word in words[1:] if word == "-" or not word.startswith("-")]
            if targets and targets[0] != "-":
                target = os.path.join(cwd, os.path.expanduser(targets[0]))
                cwd = target if os.path.isdir(target) else cwd


def outgoing(commands: list[list[str]], cwd: str) -> Iterator[Outgoing]:
    """What each ``git push`` of a shell line's simple *commands* would send, following ``cd`` from *cwd*. Raises
    ``Undetermined`` for the first push it cannot tell."""
    for words, where in in_directory(commands, cwd):
        if Path(words[0]).name == "git" and "push" in words[1:]:
            yield _outgoing(_parse(words, where))
