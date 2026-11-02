"""Shell view of a cron ``prompt`` for the lifecycle guard's referenced-script walk.

A cron prompt is natural language, not a shell command. Tokenizing all of it as shell made prose
look like commands: ``... (first-party). Sunday ...`` read as ``. Sunday`` (source a script named
``Sunday``) and a line starting with ``~/notes/a.md`` read as executing that path. The walk then
resolved those "scripts" against the working directory, and on a cloud-synced directory it refused
the job outright, so the same prompt passed or failed depending only on where it was created.

``prompt_shell_view`` keeps only the parts of a prompt that are in a shell context, so the walk
looks for referenced scripts there alone. The direct lifecycle regex still scans the whole prompt;
this view only narrows which text is tokenized for script references.

A line is in a shell context when it is:

- inside a fenced code block (any fence: code in a prompt is not prose), with a leading ``$ ``
  prompt stripped;
- a ``$ `` prompt line (the ``$ `` is stripped);
- a command line: its command word (after env assignments and wrappers such as ``sudo``) is a
  POSIX shell (``bash x.sh``, ``sh -c "..."``), or ``.``/``source`` followed by a path-shaped
  token, or a wrapper running a path-shaped token (``sudo ./x.sh``);
- a line chaining commands with ``&&`` or ``||``, which prose does not use.

A shell line whose quotes are unbalanced, or that ends in a backslash, pulls in the following
lines until it closes, so a multi-line ``bash -c "..."`` payload is scanned whole.
"""

from __future__ import annotations

import re
from typing import Optional

_FENCE = re.compile(r"^\s*(`{3,}|~{3,})")
_SHELL_PROMPT = re.compile(r"^\s*\$\s+")
_SCRIPT_SUFFIXES = (".sh", ".bash", ".zsh")
_CHAINING_OPERATORS = frozenset({"&&", "||"})
_SOURCE_COMMANDS = frozenset({".", "source"})


def _is_path_shaped(token: str) -> bool:
    """True for a token written as a path: it has a slash, starts with ``~``/``$``, or names a
    shell script by suffix. ``Sunday`` and ``code`` are words, ``./x.sh`` and ``~/x.sh`` paths."""
    return "/" in token or token.startswith(("~", "$")) or token.endswith(_SCRIPT_SUFFIXES)


def _line_tokens(line: str) -> list[str]:
    """Shell tokens of *line*; a line shlex rejects (the opening line of a multi-line quoted
    payload, or prose with a stray apostrophe) falls back to whitespace splitting."""
    from cron.lifecycle_guard import _shlex_tokens

    try:
        return _shlex_tokens(line)
    except ValueError:
        return line.split()


def _starts_shell_command(segment: list[str]) -> bool:
    """True when the segment's command word is a shell, a ``.``/``source`` of a path-shaped
    token, or a wrapper (``sudo``, ``env``, ...) running a shell or a path-shaped token."""
    from cron.lifecycle_guard import (
        _SHELL_EXECUTABLES,
        _command_token_index,
        _executable_name,
        _peel_transparent_prefixes,
    )

    index = _command_token_index(segment)
    if index is None:
        return False
    peeled = _peel_transparent_prefixes(segment, index)
    for position in dict.fromkeys((index, peeled)):
        if position >= len(segment):
            continue
        name = _executable_name(segment[position])
        if name in _SHELL_EXECUTABLES:
            return True
        if name in _SOURCE_COMMANDS and position + 1 < len(segment):
            if _is_path_shaped(segment[position + 1]):
                return True
    return peeled != index and peeled < len(segment) and _is_path_shaped(segment[peeled])


def _is_command_line(line: str) -> bool:
    """True when a prose-context line reads as a shell command line (see module docstring)."""
    from cron.lifecycle_guard import _split_segments

    tokens = _line_tokens(line)
    if any(token in _CHAINING_OPERATORS for token in tokens):
        return True
    first = next(_split_segments(tokens), None)
    return first is not None and _starts_shell_command(first)


def _needs_continuation(text: str) -> bool:
    from cron.lifecycle_guard import _shlex_tokens

    if text.endswith("\\"):
        return True
    try:
        _shlex_tokens(text)
    except ValueError:
        return True
    return False


def _strip_shell_prompt(line: str) -> str:
    return _SHELL_PROMPT.sub("", line, count=1)


def _shell_line(line: str, fence: Optional[str]) -> Optional[str]:
    """The shell text of one physical line, or None when it is prose."""
    if fence is not None:
        return _strip_shell_prompt(line)
    if _SHELL_PROMPT.match(line):
        return _strip_shell_prompt(line)
    if _is_command_line(line):
        return line
    return None


def prompt_shell_view(prompt: str) -> str:
    """Return the shell-context lines of *prompt* (see module docstring), newline-joined."""
    lines = prompt.splitlines()
    kept: list[str] = []
    fence: Optional[str] = None
    position = 0
    while position < len(lines):
        line = lines[position]
        position += 1
        fence_match = _FENCE.match(line)
        if fence_match:
            marker = fence_match.group(1)
            if fence is None:
                fence = marker
            elif marker[0] == fence[0] and len(marker) >= len(fence):
                fence = None
            continue
        shell = _shell_line(line, fence)
        if shell is None:
            continue
        while position < len(lines) and _needs_continuation(shell):
            shell = f"{shell}\n{lines[position]}"
            position += 1
        kept.append(shell)
    return "\n".join(kept)
