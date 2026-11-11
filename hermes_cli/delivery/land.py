"""``lina work merge --branch <B> --task <run id> <title> <issue>``: land a worker branch.

The branch is fetched from the worker's device as it is and ``dev`` is merged into it: commits are never re-applied
as patches (a subtree import once landed without its files that way). Before anything leaves this machine the
landing stops on: no commits over ``dev``, an author or committer that is not the project's noreply address, a
conflict outside ``CHANGELOG.md`` (both sides are kept there), a worker commit or a changed file the result lost,
an added line matching ``work.forbidden_patterns``, or a gitleaks finding. Then the branch is pushed, the pull
request opened, and the same gates as ``lina work merge <repo> <pr>`` decide. A task submitted with ``--base`` lands
on that branch instead of ``dev``.

A branch whose pull request is open takes the worker's new commits as a follow-up on that pull request: the pull
request's head merged into the worker branch when the branch lacks it (a conflict stops the landing), the same
checks, the pull request's base merged in only when the result lacks it, and a fast-forward push (never forced).
A closed or merged pull request for the branch refuses the landing.
"""
from __future__ import annotations

import re
import subprocess
import tempfile
from pathlib import Path
from typing import Protocol

from hermes_cli.delivery import settings
from hermes_cli.delivery.merge import merge_pr
from hermes_platform.resolver import locate_command

NOREPLY = "@users.noreply.github.com"


class LandError(RuntimeError):
    pass


class BranchSource(Protocol):
    def fetch(self, job: dict, clone: Path, branch: str) -> str:
        """Fetch the worker's checked-out branch into ``refs/heads/<branch>`` of ``clone``; its head sha."""


def git(*args: str, cwd: Path, check: bool = True, env: dict | None = None) -> str:
    done = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, encoding="utf-8", errors="replace",
                          check=False, env=env, timeout=600)
    if check and done.returncode != 0:
        raise LandError(f"git {args[0]} failed: {(done.stderr.strip().splitlines() or [''])[-1][:200]}")
    return done.stdout.strip()


class WorkerBranchSource:
    """The branch of a ``lina worker`` run (#35): read from the task's work folder on a local device; from a remote
    device as a git bundle of the work folder's checked-out branch through the transport (``_device bundle``)."""

    def __init__(self, base: Path | None = None):
        from hermes_cli.worker import settings as worker_settings
        self.root = worker_settings.root(base)

    def fetch(self, job: dict, clone: Path, branch: str) -> str:
        from hermes_cli.worker import device
        from hermes_cli.worker.hub import Hub
        hub = Hub(self.root, out=lambda _line: None)
        if not job.get("host"):
            raise LandError(f"{job['id']} never started on a device")
        if hub.device(job["host"]).local:
            work = device.task_of(self.root, job["id"]).work
            head = git("rev-parse", "HEAD", cwd=work)
            git("fetch", "-q", str(work), f"HEAD:refs/heads/{branch}", cwd=clone)
        else:
            from hermes_cli.worker.transport import TransportError, fetch_bundle
            with tempfile.TemporaryDirectory(prefix="lina-bundle-") as tmp:
                try:
                    bundle = fetch_bundle(hub.transport(job), job["id"], Path(tmp) / "worker.bundle")
                except TransportError as exc:
                    raise LandError(f"{job['host']}: {exc}") from None
                head = git("bundle", "list-heads", str(bundle), cwd=clone).split()[0]
                git("fetch", "-q", str(bundle), f"{head}:refs/heads/{branch}", cwd=clone)
        if git("rev-parse", f"refs/heads/{branch}", cwd=clone) != head:
            raise LandError("the fetched head differs from the work folder's head")
        return head


def _identity(base: Path | None) -> tuple[str, str] | None:
    match = re.match(r"^\s*(.*?)\s*<([^>]+)>\s*$", settings.author(base) or "")
    return (match[1], match[2]) if match else None


def check_authors(clone: Path, onto: str, branch: str, base: Path | None) -> None:
    identity = _identity(base)
    emails = set(git("log", "--format=%ae%n%ce", f"{onto}..{branch}", cwd=clone).split())
    bad = {e for e in emails if (e != identity[1] if identity else not e.endswith(NOREPLY))}
    if bad:
        raise LandError(f"{len(bad)} commit author/committer address(es) are not the project's noreply address; "
                        "re-author the commits before landing")


def merge_base_into(clone: Path, onto: str, base: Path | None) -> None:
    identity = _identity(base)
    who = ["-c", f"user.name={identity[0]}", "-c", f"user.email={identity[1]}"] if identity else []
    if subprocess.run(["git", *who, "merge", "-q", "--no-edit", onto], cwd=clone, capture_output=True,
                      check=False, timeout=600).returncode == 0:
        return
    left = git("diff", "--name-only", "--diff-filter=U", cwd=clone).split()
    if any(path != "CHANGELOG.md" for path in left):
        git("merge", "--abort", cwd=clone, check=False)
        raise LandError(f"conflict in {', '.join(p for p in left if p != 'CHANGELOG.md')}: resolve it by hand")
    path = clone / "CHANGELOG.md"
    text = _keep_both(path.read_text(encoding="utf-8-sig"))
    if text is None:
        git("merge", "--abort", cwd=clone, check=False)
        raise LandError("conflict in CHANGELOG.md that is not two plain sides: resolve it by hand")
    path.write_text(text, encoding="utf-8")
    git("add", "CHANGELOG.md", cwd=clone)
    git(*who, "commit", "-q", "--no-edit", cwd=clone)


_CONFLICT = re.compile(r"<<<<<<< [^\n]*\n(.*?)=======\n(.*?)>>>>>>> [^\n]*\n", re.DOTALL)
_MARKER = re.compile(r"^(?:<<<<<<<|=======|>>>>>>>|\|\|\|\|\|\|\|)(?: |$)", re.MULTILINE)


def _keep_both(text: str) -> str | None:
    """Both sides of each CHANGELOG conflict, ours first, or None when a hunk is not the plain two-way kind
    (a diff3 base section, CRLF lines) or any marker is left: a guess there would push conflict text."""
    if "\r\n" in text or "\n||||||| " in text:
        return None
    merged = _CONFLICT.sub(lambda m: m[1] + m[2], text)
    return None if _MARKER.search(merged) else merged


def check_result(clone: Path, onto: str, worker_head: str, base: Path | None) -> None:
    for commit, what in ((onto, "dev"), (worker_head, "the worker branch")):
        if subprocess.run(["git", "merge-base", "--is-ancestor", commit, "HEAD"], cwd=clone,
                          check=False, timeout=120).returncode != 0:
            raise LandError(f"the result does not contain {what}")
    wanted = set(git("diff", "--name-only", f"{onto}...{worker_head}", cwd=clone).split())
    landed = set(git("diff", "--name-only", onto, "HEAD", cwd=clone).split())
    if wanted - landed:
        raise LandError(f"the landing lost {len(wanted - landed)} changed file(s) of the worker branch")
    added = [line[1:] for line in git("diff", onto, "HEAD", cwd=clone).splitlines()
             if line.startswith("+") and not line.startswith("+++")]
    patterns = [re.compile(p, re.IGNORECASE) for p in settings.forbidden_patterns(base)]
    hits = sum(1 for line in added if any(p.search(line) for p in patterns))
    if hits:
        raise LandError(f"{hits} added line(s) match work.forbidden_patterns")
    gitleaks = locate_command("gitleaks")
    if gitleaks.found:
        done = subprocess.run([*gitleaks.command, "git", f"--log-opts={onto}..HEAD", "--no-banner", "--redact"], cwd=clone,
                              capture_output=True, check=False, timeout=600)
        if done.returncode != 0:
            raise LandError("gitleaks found a secret in the branch")


def _body(job_out: Path, branch_log: str, issue: int) -> str:
    body_file = job_out / "out" / "pr-body.md"
    body = body_file.read_text(encoding="utf-8-sig") if body_file.is_file() else f"## Summary\n\n{branch_log}\n"
    if not re.search(rf"\bCloses #{issue}\b", body):
        body = body.rstrip("\n") + f"\n\nCloses #{issue}\n"
    return body


def _contains(clone: Path, commit: str, head: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", commit, head], cwd=clone, check=False,
                          timeout=120).returncode == 0


def _open_pull(pulls: list, branch: str) -> dict:
    """The open pull request whose head is *branch*; any other pull request for the branch refuses the landing."""
    for pull in pulls:
        if pull.get("state") == "open" and (pull.get("head") or {}).get("ref") == branch:
            return pull
    raise LandError(f"a pull request for {branch} already exists")


def _merge_pull_head(clone: Path, pushed: str, number: int, base: Path | None) -> None:
    """The pull request's head (often the first landing's merge of its base) into the worker branch."""
    identity = _identity(base)
    who = ["-c", f"user.name={identity[0]}", "-c", f"user.email={identity[1]}"] if identity else []
    if subprocess.run(["git", *who, "merge", "-q", "--no-edit", pushed], cwd=clone, capture_output=True,
                      check=False, timeout=600).returncode == 0:
        return
    left = git("diff", "--name-only", "--diff-filter=U", cwd=clone).split()
    git("merge", "--abort", cwd=clone, check=False)
    raise LandError(f"conflict with pull request #{number} in {', '.join(left) or 'the merge'}: resolve it by hand")


def follow_up(gh, hub, job: dict, pull: dict, branch: str, *, base: Path | None, source, out, **merge_options) -> int:
    """The worker's new commits onto the open pull request *pull* of *branch*, pushed as a fast-forward."""
    from hermes_cli.worker import settings as worker_settings
    repo, number, onto_branch = job["repo"], int(pull["number"]), pull["base"]["ref"]
    url = str(worker_settings.setting("repo_url", worker=hub.worker)).format(repo=repo)
    with tempfile.TemporaryDirectory(prefix="lina-land-") as tmp:
        clone = Path(tmp) / "repo"
        git("clone", "-q", "-b", onto_branch, url, str(clone), cwd=Path(tmp))
        onto, pushed = f"origin/{onto_branch}", git("rev-parse", f"refs/remotes/origin/{branch}", cwd=clone)
        worker_head = (source or WorkerBranchSource(base)).fetch(job, clone, branch)
        git("switch", "-q", branch, cwd=clone)
        if not git("merge-base", pushed, branch, cwd=clone, check=False):
            raise LandError(f"{branch} has no common ancestor with pull request #{number}")
        count = int(git("rev-list", "--count", f"{pushed}..{branch}", cwd=clone))
        if count == 0:
            raise LandError(f"{branch} has no commits over pull request #{number}")
        if not _contains(clone, pushed, branch):
            _merge_pull_head(clone, pushed, number, base)  # the result then descends from it: a fast-forward
        check_authors(clone, onto, branch, base)
        merged = not _contains(clone, onto, branch)
        if merged:
            merge_base_into(clone, onto, base)
        check_result(clone, onto, worker_head, base)
        out(f"{branch}: {count} worker commit(s) added to #{number} as they are"
            + (f", {onto_branch} merged in" if merged else ""))
        git("push", "-q", "origin", f"{branch}:refs/heads/{branch}", cwd=clone)
    return merge_pr(gh, repo, number, base=base, out=out, **merge_options)


def land(gh, branch: str, task: str, title: str, issue: int, *, base: Path | None = None, source=None, out=print,
         **merge_options) -> int:
    from hermes_cli.worker import settings as worker_settings
    from hermes_cli.worker.hub import Hub
    hub = Hub(worker_settings.root(base), out=lambda _line: None)
    job = hub.need(task)
    repo = job.get("repo")
    if not repo or "/" not in repo:
        raise LandError(f"{task} has no owner/name repository")
    pulls = gh.json(f"repos/{repo}/pulls?state=all&head={repo.split('/')[0]}:{branch}")
    if pulls:
        return follow_up(gh, hub, job, _open_pull(pulls, branch), branch, base=base, source=source, out=out,
                         **merge_options)
    onto_branch = job.get("base_branch") or settings.base_branch(base)
    url = str(worker_settings.setting("repo_url", worker=hub.worker)).format(repo=repo)
    with tempfile.TemporaryDirectory(prefix="lina-land-") as tmp:
        clone = Path(tmp) / "repo"
        git("clone", "-q", "-b", onto_branch, url, str(clone), cwd=Path(tmp))
        onto = f"origin/{onto_branch}"
        worker_head = (source or WorkerBranchSource(base)).fetch(job, clone, branch)
        git("switch", "-q", branch, cwd=clone)
        count = int(git("rev-list", "--count", f"{onto}..{branch}", cwd=clone))
        if count == 0:
            raise LandError(f"{branch} has no commits over {onto_branch}")
        check_authors(clone, onto, branch, base)
        merge_base_into(clone, onto, base)
        check_result(clone, onto, worker_head, base)
        out(f"{branch}: {count} worker commit(s) kept as they are, {onto_branch} merged in")
        git("push", "-q", "origin", f"{branch}:refs/heads/{branch}", cwd=clone)
        log = "\n".join(f"- {s}" for s in git("log", "--no-merges", "--format=%s", f"{onto}..HEAD",
                                                cwd=clone).splitlines())
        body_file = Path(tmp) / "body.md"
        body_file.write_text(_body(hub.store.out_dir(job), log, issue), encoding="utf-8")
        gh.run(["pr", "create", "-R", repo, "--base", onto_branch, "--head", branch, "--title", title,
                "--body-file", str(body_file)])
    number = int(gh.json(f"repos/{repo}/pulls?state=open&head={repo.split('/')[0]}:{branch}")[0]["number"])
    out(f"{branch}: opened #{number}")
    return merge_pr(gh, repo, number, base=base, out=out, **merge_options)
