"""Portable profile archives and their private-state filters."""

import logging
import os
import re
import shutil
import stat
import time
from pathlib import Path
from typing import Optional

from agent.file_safety import HOME_CREDENTIAL_DIRS
from hermes_cli.home_data_layout import PM_RUNTIME_ROOT_DIRS, SECRET_CACHE_STORES
from hermes_cli.archive_safe import archive_root_dirs, make_targz, normalize_archive_parts, safe_extract_targz

logger = logging.getLogger("hermes_cli.profiles")

from hermes_constants import LOCAL_RUNTIME_ROOT_DIRS

# Infrastructure excluded from --clone-all ONLY when the source is the default profile
# (``~/.hermes``): git checkout (+ ~3 GB venv), worktrees, sibling profiles, shared bins,
# npm packages, and the managed local-models trees — GGUF weights (tens of GB), the
# llama.cpp runtime binaries and the managed Node install, all re-downloadable on demand
# and resolved from the default root only. Named profiles never hold these at root, so the
# gate avoids silently dropping user data from a named-profile source. Export uses a root
# allow-list instead (``_DEFAULT_EXPORT_INCLUDE_ROOT``): an archive is a portable snapshot,
# a clone must run. The runtime trio is ``LOCAL_RUNTIME_ROOT_DIRS``, shared with
# ``hermes_cli.backup._EXCLUDED_ROOT_DIRS`` so the two lists cannot drift.
_CLONE_ALL_DEFAULT_EXCLUDE_ROOT: frozenset[str] =  frozenset({
    "hermes-agent",
    ".worktrees",
    "profiles",
    "bin",
    "node_modules",
    # Managed runtime trees — install artifacts, never profile state
    # (install/profile bucket split; see test_install_bucket_separation).
    ".hermes-runtime",
    "node",
}) | LOCAL_RUNTIME_ROOT_DIRS

# Per-profile history excluded from --clone-all for ANY source: SQLite session store
# (+wal/shm, can reach many GB), session dirs, `hermes backup` archives, quick-backup
# snapshots, checkpoints. Inheriting them is never useful (restoring one inside the
# clone would resurrect the SOURCE profile's state) and can balloon the copy by tens of GB.
# ``cron`` is scheduled work bound to the source profile and its origin channel: a clone
# that inherits jobs.json runs every job twice (two gateways, same job ids, double spend,
# duplicate deliveries) the moment its gateway starts. The empty dir is recreated below.
_CLONE_ALL_HISTORY_EXCLUDE_ROOT: frozenset[str] = frozenset({
    "state.db", "state.db-wal", "state.db-shm", "sessions", "backups", "state-snapshots", "checkpoints",
    "cron", "webapp",  # webapp/: the source gateway's sign-in state (credential, native grants, Portal callback)
})


def _clone_all_copytree_ignore(source_dir: Path):
    """copytree ignore for --clone-all: history artifacts for any source, infrastructure
    only when the source is the default profile (see the two exclude sets above)."""
    from hermes_cli.profiles import _get_default_hermes_home
    source_resolved = source_dir.resolve()
    root_exclude = set(_CLONE_ALL_HISTORY_EXCLUDE_ROOT)
    if source_resolved == _get_default_hermes_home().resolve():
        root_exclude |= _CLONE_ALL_DEFAULT_EXCLUDE_ROOT

    def _ignore(directory: str, names: list[str]) -> set:
        try:
            at_root = Path(directory).resolve() == source_resolved
        except (OSError, ValueError):
            # resolve() can fail on odd FS layouts (broken symlinks, missing parents).
            # Fail open — better to over-copy than silently drop user data.
            at_root = False
        ignored = _non_exportable_entries(directory, names)
        if at_root:
            ignored.update(root_exclude & set(names))
        return ignored

    return _ignore

def _non_exportable_entries(directory: str, contents: list) -> set:
    """Entries under *directory* that must never be copied out of a profile: bytecode caches,
    ``*.sock``/``*.tmp`` names, and anything that is not a regular file, directory, or symlink.
    :func:`shutil.copytree` cannot copy special files, so a single live Unix socket without a
    ``.sock`` name (or a FIFO, or a device node) would abort the whole export or clone with
    ``[Errno 6] No such device or address``. Symlinks survive — copytree recreates them."""
    ignored: set = set()
    for entry in contents:
        if entry == "__pycache__" or entry.endswith((".sock", ".tmp", ".pyc", ".pyo")):
            ignored.add(entry)
            continue
        try:
            mode = os.lstat(os.path.join(directory, entry)).st_mode
        except OSError:
            ignored.add(entry)  # vanished mid-walk — copytree would fail on it anyway
            continue
        if not (stat.S_ISREG(mode) or stat.S_ISDIR(mode) or stat.S_ISLNK(mode)):
            ignored.add(entry)
    return ignored

# OS credential dirs (file_safety.HOME_CREDENTIAL_DIRS) + direnv .envrc (_BLOCKED_PROJECT_ENV_BASENAMES).
_OS_CREDENTIAL_STORES = (*HOME_CREDENTIAL_DIRS, ".envrc")

# Credential stores in a profile home, as paths relative to it, plus the directories where Hermes
# keeps recovery copies of them. Never shipped in a profile export, and user-owned (never
# overwritten) on a distribution install. Add a store here when a writer or loader starts using one.
PROFILE_CREDENTIAL_PATHS = frozenset({
    "auth.json", ".env", "auth/google_oauth.json",
    ".op.env",                      # 1Password service-account token (env_loader)
    "npmrc",                        # npm registry auth (source_build)
    ".anthropic_oauth.json",        # Anthropic OAuth tokens (credential_sources)
    "google_token.json", "google_oauth_pending.json", "google_client_secret.json",
    "google_chat_user_tokens", "google_chat_user_client_secret.json", "google_chat_user_oauth_pending.json",
    "google_chat_user_token.json", "google_chat_user_oauth_pending",  # legacy single-user layouts
    "slack_tokens.json",
    "honcho.json",                  # Honcho apiKey + OAuth grant (oauth.refreshToken)
    "mem0.json",                    # Mem0 api_key (self-hosted server key) next to its settings
    "webhook_subscriptions.json",   # per-route HMAC secrets
    "teams_pipeline_store.json",    # Graph subscription client_state (webhook shared secret)
    "mcp-tokens",                   # MCP OAuth tokens
    "vault",                        # vault.key + vault.json.enc
    "browser-profile", "browser_auth", "bot-desktop", "webapp",  # browser cookies / logins, Webapp sign-in
    "browser-profiles", "browser_profiles",  # live CDP profiles, Browser Use CLI dir (Cookies, Login Data)
    "pairing", "platforms/pairing", "feishu_comment_pairing.json",
    "whatsapp/session", "platforms/whatsapp/session", "matrix/store", "platforms/matrix/store",
    *SECRET_CACHE_STORES,           # Bitwarden / 1Password value caches, Photon sidecar token
    "workspace/meetings/node_token.json",  # google_meet node RPC secret
    "weixin/accounts",              # WeChat bot tokens + per-peer context tokens
    ".copilot_jwt.json",            # exchanged Copilot API token
    "proxy",                        # iron-proxy CA key + proxy tokens
    "chrome-debug",                 # /browser connect Chrome profile (cookies, logins)
    "home",                         # subprocess HOME: gh, git, ssh, npm and skill-CLI credentials
    "backups", "state-snapshots",   # pre-update zips, config copies, update snapshots of the stores
    *_OS_CREDENTIAL_STORES,
})
_CREDENTIAL_PATH_PARTS = tuple(tuple(p.casefold().split("/")) for p in PROFILE_CREDENTIAL_PATHS)

# Copies Hermes' writers leave beside a store at the profile root, matched case-folded. Any
# ``auth.json.*`` / ``.env.bak*`` is a credential store whoever named it; for config.yaml only the
# writers' formats match (post_update ``.bak-<stamp>[.N]``, config_backups' legacy siblings), because a
# hand-named ``config.yaml.bak-my-note`` is the user's and ships through the export scrub instead.
_STORE_COPY_RE = re.compile(
    r"auth\.json\..+|\.env\.bak.*"
    r"|config\.yaml\.(?:bak-\d{8}t\d{6}z(?:\.\d+)?|bak\.\d+|corrupt\..*|bak-pre-migrate-.*)"
)


def _fold(parts: tuple[str, ...]) -> tuple[str, ...]:
    return tuple(part.casefold() for part in parts)


def profile_path_is_private(parts: tuple[str, ...]) -> bool:
    """True for a PROFILE_CREDENTIAL_PATHS store, anything below one, or a root copy of one.
    Case-folded: on a case-insensitive filesystem ``Platforms/Pairing`` IS the pairing store."""
    folded = _fold(parts)
    if len(folded) == 1 and _STORE_COPY_RE.fullmatch(folded[0]):
        return True
    return any(folded[:len(store)] == store for store in _CREDENTIAL_PATH_PARTS)


def profile_path_contains_private_store(parts: tuple[str, ...]) -> bool:
    """True for a strict ancestor of a store (``platforms`` holds ``platforms/pairing``)."""
    folded = _fold(parts)
    return any(len(store) > len(folded) and store[:len(folded)] == folded for store in _CREDENTIAL_PATH_PARTS)

# Directories/files to exclude when exporting the default (~/.hermes) profile.
# The default profile contains infrastructure (repo checkout, worktrees, DBs,
# caches, binaries) that named profiles don't have.  We exclude those so the
# export is a portable, reasonable-size archive of actual profile data.
# The ONE exclude-list authority for "infrastructure / runtime state that is
# not profile data". The export path (``_default_export_ignore``) consumes it
# directly; the distribution path (``profile_distribution.USER_OWNED_EXCLUDE``)
# layers its distribution-specific additions on top of it. Keep new root
# artifacts here — not in a downstream copy.
_DEFAULT_EXPORT_EXCLUDE_ROOT = DEFAULT_EXPORT_EXCLUDE_ROOT = frozenset({
    # Infrastructure
    "hermes-agent",         # repo checkout (multi-GB)
    ".worktrees",           # git worktrees
    "profiles",             # other profiles — never recursive-export
    "bin",                  # installed binaries
    "node_modules",         # npm packages
    ".hermes-runtime",      # managed runtime tree (install artifact)
    "node",                 # legacy pre-split managed Node tree
    # Databases & runtime state
    "state.db", "state.db-shm", "state.db-wal",
    "hermes_state.db",
    "response_store.db", "response_store.db-shm", "response_store.db-wal",
    "gateway.pid", "gateway_state.json", "processes.json",
    "auth.json",            # API keys, OAuth tokens, credential pools
    ".env",                 # API keys (dotenv)
    "auth.lock", "active_profile", ".update_check", "source-checks",
    "errors.log",
    ".hermes_history",
    # Caches (regenerated on use)
    "image_cache", "audio_cache", "document_cache",
    "browser_screenshots", "checkpoints",
    "sandboxes",
    "logs",                 # gateway logs
}) | PM_RUNTIME_ROOT_DIRS

# Allow-list for ``export_profile("default")``: when HERMES_HOME equals the
# cwd (Docker/custom deployments), the default profile home is the working
# directory and contains arbitrary user files that should NOT be bundled
# into the export. The set below identifies the *known Hermes profile
# artifacts* at the root of HERMES_HOME; everything else is excluded.
# Sensitive runtime infrastructure (``state.db``, ``logs/``, ``auth.*``,
# other profiles) is intentionally *not* in this list so the export stays
# a portable, credential-free snapshot of the user-facing surface
# (#58394). Add new artifacts here when introduced in ``hermes_constants``.
_DEFAULT_EXPORT_INCLUDE_ROOT = frozenset({
    # Configuration / persona
    "config.yaml", "SOUL.md", "MEMORY.md", "USER.md", "todo.json",
    "system_prompt.md", "AGENTS.md", "CLAUDE.md", ".cursorrules",
    # Desktop appearance overlay (written/applied by the desktop app's export/import).
    "desktop.json",
    # User-facing skill, cron, and session artifacts
    "skills", "cron", "scripts", "sessions",
    # Plugin / memory surfaces (per-profile overrides live here)
    "plugins", "memories", "knowledge", "preferences",
})

# Export / Import

def _inside_git_checkout(path: Path) -> bool:
    """True when *path* lies inside a Git checkout. Walks the path's OWN resolved ancestry
    (not cwd) so the check holds when HERMES_HOME sits in a checkout but the process runs
    elsewhere (cron, service manager). Resolution failure reports True (fail closed)."""
    try:
        resolved = path.resolve()
    except (OSError, RuntimeError):  # RuntimeError: symlink loops on Python <= 3.12
        return True
    return any((candidate / ".git").exists() for candidate in (resolved, *resolved.parents))


def _profile_export_directory() -> Path:
    """Choose an export directory that cannot become source-tree input."""
    import tempfile
    from hermes_cli.profiles import _get_default_hermes_home
    export_dir = _get_default_hermes_home() / "profile-exports"
    if not _inside_git_checkout(export_dir):
        return export_dir

    # A custom deployment may point HERMES_HOME at its source checkout: use a sibling store,
    # falling back to the OS temp dir only when the user's home itself is a checkout (dotfiles
    # repo). Per-uid temp name: a fixed /tmp/hermes-profile-exports is a predictable shared
    # path another local user could pre-create (or symlink) first.
    uid_suffix = f"-{os.getuid()}" if hasattr(os, "getuid") else ""
    candidates = (
        Path.home() / ".hermes-profile-exports", Path(tempfile.gettempdir()) / f"hermes-profile-exports{uid_suffix}"
    )
    for candidate in candidates:
        if not _inside_git_checkout(candidate):
            return candidate
    # Fail closed: writing a secret-bearing archive into a source tree is the incident this
    # helper prevents; a stderr warning would not stop a scripted export.
    raise ValueError(
        # See #92457.
        "No safe automatic export destination: every candidate directory is "
        "inside a Git checkout. Provide an explicit output path outside the "
        "checkout (CLI: -o /path/outside/repo/profile.tar.gz)."
    )


def get_profile_export_path(name: str, *, timestamp: Optional[str] = None) -> Path:
    """Managed destination for an export with no explicit output — outside the cwd and every
    profile, since a ``<name>.tar.gz`` default in a source checkout got committed by accident."""
    from hermes_cli.profiles import _canon_valid
    canon = _canon_valid(name)
    export_dir = _profile_export_directory()
    export_dir.mkdir(parents=True, exist_ok=True)
    # exist_ok=True silently accepts a directory (or symlink) another local user pre-created
    # at a predictable path; refuse to write a secret-bearing archive anywhere we don't own.
    if export_dir.is_symlink():
        raise ValueError(
            f"Export directory {export_dir} is a symlink; refusing to write "
            "a profile archive through it. Provide an explicit output path."
        )
    if hasattr(os, "getuid") and export_dir.stat().st_uid != os.getuid():
        raise ValueError(
            f"Export directory {export_dir} is owned by another user; "
            "refusing to write a profile archive there. Provide an explicit output path."
        )
    stamp = timestamp or time.strftime("%Y%m%d-%H%M%S")
    return export_dir / f"{canon}-{stamp}.tar.gz"


def _default_export_ignore(root_dir: Path):
    """copytree ignore for the default-profile export: root-level allow-list
    (``_DEFAULT_EXPORT_INCLUDE_ROOT``) plus universal exclusions. Surviving text files are
    then force-redacted by :func:`_scrub_export_secrets`.

    * **Root-level allow-list** — only entries whose name appears in ``_DEFAULT_EXPORT_INCLUDE_ROOT``
    survive. Everything else (such as an unrelated ``x11-dev/`` directory in a Docker deployment where
    HERMES_HOME equals the cwd) is excluded. Blacklisting was tried first and proved unable to anticipate
    every non-Hermes file the user may have lying alongside HERMES_HOME (#58394). * **Universal exclusions
    at any depth** — ``__pycache__``, sockets and other special files, temp files
    (:func:`_non_exportable_entries`); plus npm lockfiles, which may appear at the root.
    """

    def _ignore(directory: str, contents: list) -> set:
        # Universal exclusions and credential names (any depth) plus npm lockfiles that can appear at root.
        ignored = _non_exportable_entries(directory, contents)
        ignored.update({"package.json", "package-lock.json"} & set(contents))
        ignored.update(_export_credential_entries(directory, contents))
        if Path(directory) == root_dir:
            ignored.update(entry for entry in contents if entry not in _DEFAULT_EXPORT_INCLUDE_ROOT)
        return ignored

    return _ignore


# Credential names dropped at ANY depth of every profile export, on top of the root-relative
# PROFILE_CREDENTIAL_PATHS. ``bot-desktop`` is the screen's runtime state:
# its persistent Chromium profile (Cookies, Login Data — the bot's live web sessions), Xauthority, sockets.
# The OS stores are dropped wherever they sit (a skill dir copied from a home carries its ``.ssh``).
_EXPORT_CREDENTIAL_FILES = frozenset({"auth.json", ".env", "bot-desktop", *_OS_CREDENTIAL_STORES})
_EXPORT_CREDENTIAL_PARTS = tuple(tuple(p.split("/")) for p in _EXPORT_CREDENTIAL_FILES)


def _export_credential_entries(directory: str, contents: list) -> set:
    """Entries of *directory* that are an _EXPORT_CREDENTIAL_FILES store: matched on trailing path
    components, so ``.config/gh`` drops at any depth while the rest of ``.config`` ships."""
    parts = Path(directory).parts
    return {entry for entry in contents for store in _EXPORT_CREDENTIAL_PARTS
            if entry == store[-1] and parts[len(parts) + 1 - len(store):] == store[:-1]}

# Text/config suffixes secret-scrubbed on export; binary DBs, images etc. are left alone.
_EXPORT_REDACT_SUFFIXES = frozenset({
    ".md", ".txt", ".yaml", ".yml", ".json", ".jsonl", ".toml", ".ini", ".cfg", ".conf", ".py", ".sh",
    ".bash", ".zsh", ".js", ".ts", ".tsx", ".jsx", ".css", ".html", ".xml", ".csv",
})
# ``Path(".cursorrules").suffix`` is "" — name-match; ``*.env.example`` uses endswith; a hand-named
# config copy (``config.yaml.bak-my-note``) holds config.yaml's secrets under a suffix of its own.
_EXPORT_REDACT_NAMES = frozenset({".cursorrules"})


def _should_redact_export_file(path: Path) -> bool:
    name = path.name
    return (
        name in _EXPORT_REDACT_NAMES
        or name.lower().endswith(".env.example")
        or name.lower().startswith("config.yaml.")
        or path.suffix.lower() in _EXPORT_REDACT_SUFFIXES
    )


def _scrub_export_secrets(staged: Path) -> None:
    """Force-redact secret-shaped strings in a staged export tree (same pass as ``hermes
    sessions export --redact``). Runs on the staged copy only; symlinks to text files are
    materialized when content changes so redaction never follows a link back into the source."""
    from agent.redact import redact_sensitive_text
    for path in staged.rglob("*"):
        try:
            is_link = path.is_symlink()
            if not path.is_file():  # broken links, symlinked dirs, non-files
                continue
        except OSError:
            continue
        if not _should_redact_export_file(path):
            continue
        try:
            text = path.read_text(encoding="utf-8-sig")
        except (UnicodeDecodeError, OSError):
            continue
        redacted = redact_sensitive_text(text, force=True)
        if redacted == text:
            continue
        if is_link:
            path.unlink()
        path.write_text(redacted, encoding="utf-8")


def export_profile(name: str, output_path: str, extra_files: Optional[dict[str, str]] = None) -> Path:
    """Export a profile to a tar.gz archive; credential files are excluded and staged text is
    force-redacted first. Returns the output file path."""
    import tempfile
    from hermes_cli.profiles import _existing_profile_dir
    canon, profile_dir = _existing_profile_dir(name)
    # Archive base name without extension (.tar.gz appended by the writer).
    base = str(Path(output_path)).removesuffix(".tar.gz").removesuffix(".tgz")

    # The default profile IS ~/.hermes (dir name ".hermes"), so both paths stage a filtered
    # copy under a temp dir named after the canonical id: root allow-list for default,
    # credential exclusion for named profiles.
    def _ignore_credentials(directory: str, contents: list) -> set:
        ignored = _non_exportable_entries(directory, contents)
        ignored.update(_export_credential_entries(directory, contents))
        rel = Path(directory).relative_to(profile_dir).parts
        ignored.update(e for e in contents if profile_path_is_private((*rel, e)))
        if Path(directory) == profile_dir:
            ignored |= PM_RUNTIME_ROOT_DIRS & set(contents)
        return ignored

    ignore = _default_export_ignore(profile_dir) if canon == "default" else _ignore_credentials
    with tempfile.TemporaryDirectory() as tmpdir:
        staged = Path(tmpdir) / canon
        shutil.copytree(profile_dir, staged, symlinks=True, ignore=ignore)
        for rel, content in (extra_files or {}).items():
            target = staged.joinpath(*normalize_archive_parts(rel))
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(content, encoding="utf-8")
        _scrub_export_secrets(staged)
        from hermes_cli.backup_message_text import export_profile_texts
        export_profile_texts(profile_dir, staged)
        return Path(make_targz(base, tmpdir, canon))


def import_profile(archive_path: str, name: Optional[str] = None) -> Path:
    """Import a profile from a tar.gz archive."""
    import tempfile
    archive = Path(archive_path)
    if not archive.exists():
        raise FileNotFoundError(f"Archive not found: {archive}")
    top_dirs = archive_root_dirs(archive)
    archive_root = top_dirs.pop() if len(top_dirs) == 1 else None
    inferred_name = name or archive_root
    if not inferred_name:
        raise ValueError(
            "Cannot determine profile name from archive. "
            "Specify it explicitly: hermes profile import <archive> --name <name>"
        )
    if archive_root is None:
        raise ValueError("Profile archive must contain exactly one top-level directory.")

    # Default-profile archives have "default/" at top level; importing as "default" would
    # target ~/.hermes itself.
    from hermes_cli.profiles import _canon_valid, get_profile_dir, _profile_exists_error, _get_profiles_root
    canon = _canon_valid(inferred_name)
    if canon == "default":
        raise ValueError(
            "Cannot import as 'default' — that is the built-in root profile (~/.hermes). "
            "Specify a different name: hermes profile import <archive> --name <name>"
        )
    profile_dir = get_profile_dir(canon)
    if profile_dir.exists():
        raise _profile_exists_error(canon)
    _get_profiles_root().mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="hermes_profile_import_") as tmpdir:
        staging_root = Path(tmpdir)
        safe_extract_targz(archive, staging_root)
        extracted = staging_root / archive_root
        if not extracted.is_dir():
            raise ValueError(f"Profile archive root is missing or invalid: {archive_root}")
        final_source = extracted
        if archive_root != canon:
            final_source = staging_root / canon
            extracted.rename(final_source)
        # Remove foreign runtime state before publishing, including file-shaped roots.
        # A failed removal must abort import rather than install a stale PM selection.
        for child in final_source.iterdir():
            if child.name in PM_RUNTIME_ROOT_DIRS:
                if child.is_dir():
                    shutil.rmtree(child)
                else:
                    child.unlink()
        shutil.move(str(final_source), str(profile_dir))
        from hermes_cli.backup_message_text import restore_snapshot_texts
        restore_snapshot_texts(profile_dir / "state.db", profile_dir / "state.db")
    return profile_dir

