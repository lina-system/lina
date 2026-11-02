"""The memory store's install, backup, restore and upgrade steps (lina#11).

Docker is faked by a runner that keeps the state the real store would have (running services, volumes, tables),
so each test checks the order of the steps and what is left behind, not the shape of a command line.
"""

from __future__ import annotations

import json
import stat
import subprocess

import pytest

from hermes_cli import store as st
from hermes_cli import store_backup as sb
from hermes_cli import store_upgrade as su


class FakeDocker:
    def __init__(self, *, volume_exists=False, fail=()):
        self.steps, self.fail = [], set(fail)
        self.running, self.volumes = set(), {"lina-store-pgdata"} if volume_exists else set()
        # tag -> image id. As in Docker's containerd image store, an image with no tag left is gone.
        self.images, self.builds, self.writer_image = {}, 0, None
        self.tables = {"memory.t": [2, "abc"]}
        self.restored_tables = None

    def _done(self, argv, out="", code=0):
        return subprocess.CompletedProcess(argv, code, out, "")

    def __call__(self, argv, *, env=None, capture=False, stdout=None, input=None):
        if argv[:2] == ["docker", "compose"]:
            return self._compose(argv, argv[argv.index("--env-file") + 2:], env or {}, stdout, input)
        step = " ".join(argv[1:3])
        self.steps.append(step)
        if step in self.fail:
            return self._done(argv, code=1)
        if argv[1:3] == ["volume", "inspect"]:
            return self._done(argv, code=0 if argv[3] in self.volumes else 1)
        if argv[1:3] == ["image", "inspect"]:
            name = argv[-1]
            image = self.images.get(name) or (name if name in self.images.values() else None)
            return self._done(argv, image or "", code=0 if image else 1)
        if argv[1] == "tag":
            self.images[argv[3]] = self.images.get(argv[2], argv[2])
        return self._done(argv)

    def _compose(self, argv, args, env, stdout, sql):
        project = argv[argv.index("-p") + 1]
        words = [a for a in args if not a.startswith("-") or a == "--profile"]
        step = f"{project}: " + " ".join(words[:3] if args[0] == "exec" else words)
        if args[0] == "ps":
            return self._done(argv, "\n".join(sorted(self.running)) if project == st.PROJECT else "")
        if args[0] == "exec" and "psql" in args:
            return self._done(argv, self._psql(sql or ""))
        self.steps.append(step)
        if any(step.startswith(f) for f in self.fail):
            return self._done(argv, code=1)
        if args[0] == "exec" and "pg_basebackup" in args:
            stdout.write(b"cluster-bytes")
        elif args[0] == "build":
            self.builds += 1
            self.images[st.HONCHO_IMAGE] = f"sha256:build{self.builds}"
        elif args[0] == "up" and project == st.PROJECT:
            self.running |= {a for a in args if a in ("db", *st.WRITERS)}
            if set(st.WRITERS) & set(args):
                self.writer_image = self.images.get(st.HONCHO_IMAGE)
        elif args[0] == "stop":
            self.running -= set(args[1:])
        elif args[0] == "run" and "--entrypoint" in args:
            self.volumes.add(env["LINA_STORE_VOLUME"])
        return self._done(argv)

    def _psql(self, sql):
        if sql.startswith("SELECT format("):
            return "\n".join(self.tables)
        if "UNION ALL" in sql or sql.startswith("SELECT 'memory"):
            tables = self.restored_tables if self.restored_tables is not None else self.tables
            return "\n".join(f"{name}|{count}|{digest}" for name, (count, digest) in tables.items())
        if "server_version" in sql:
            return "15.14"
        if "alembic_version" in sql:
            return "e4eba9cfaa6f"
        return ""


def ok_http(method, url, body=None):
    return 200, b'{"status": "ok"}' if url.endswith("/health") else b"{}"


@pytest.fixture
def honcho_src(tmp_path, monkeypatch):
    src = tmp_path / "services" / "honcho"
    src.mkdir(parents=True)
    (src / "Dockerfile").write_text("FROM scratch\n")
    monkeypatch.setattr(st, "HONCHO_DIR", src)
    return src


def make_store(tmp_path, docker, lines):
    return st.Store(tmp_path / "store", run=docker, http=ok_http, out=lines.append)


def test_env_is_created_once_with_private_random_passwords(tmp_path):
    first = st.ensure_env(tmp_path / "store")
    again = st.ensure_env(tmp_path / "store")
    assert first == again
    assert len({first[key] for key in st.SECRET_KEYS}) == 3
    assert stat.S_IMODE((tmp_path / "store" / "store.env").stat().st_mode) == 0o600
    assert first["LINA_HONCHO_ENV"] == str(tmp_path / "store" / "honcho.env")


def test_fresh_install_builds_then_migrates_once_before_starting_honcho(tmp_path, honcho_src):
    docker, lines = FakeDocker(), []
    st.install(make_store(tmp_path, docker, lines))
    compose = [s for s in docker.steps if s.startswith("lina-store:")]
    assert compose == ["lina-store: build honcho-api", "lina-store: up db",
                       "lina-store: --profile job run honcho-migrate", "lina-store: up honcho-api honcho-deriver"]
    assert docker.running == {"db", "honcho-api", "honcho-deriver"}
    assert any("Smoke test passed" in line for line in lines)


def test_install_without_docker_says_what_to_do(tmp_path, honcho_src, monkeypatch):
    docker, lines = FakeDocker(fail={"info --format"}), []
    monkeypatch.setattr(st, "store_dir", lambda: tmp_path / "store")
    real_store = st.Store
    monkeypatch.setattr(st, "Store", lambda directory, **kw: real_store(directory, run=docker, **kw))
    assert st.install_step(out=lines.append) == 1
    assert "Docker is not running or not installed" in lines[0]


def test_install_without_a_docker_executable_says_what_to_do(tmp_path, honcho_src, monkeypatch):
    empty = tmp_path / "empty-bin"
    empty.mkdir()
    monkeypatch.setenv("PATH", str(empty))
    monkeypatch.setattr(st, "store_dir", lambda: tmp_path / "store")
    lines = []
    assert st.install_step(out=lines.append) == 1
    assert lines[0] == f"Store: {st.DOCKER_MISSING}"


def test_install_refuses_without_the_honcho_source(tmp_path, monkeypatch):
    monkeypatch.setattr(st, "HONCHO_DIR", tmp_path / "missing")
    with pytest.raises(st.StoreError, match="Honcho server source"):
        st.install(make_store(tmp_path, FakeDocker(), []))


def test_backup_quiesces_honcho_and_publishes_only_a_complete_generation(tmp_path):
    docker, lines = FakeDocker(), []
    store = make_store(tmp_path, docker, lines)
    st.ensure_env(store.directory)
    docker.running = {"db", "honcho-api", "honcho-deriver"}
    generation = sb.backup(store)
    assert [s for s in docker.steps if "stop" in s or "basebackup" in s or " up " in s] == [
        "lina-store: stop honcho-api honcho-deriver", "lina-store: exec db pg_basebackup",
        "lina-store: up honcho-api honcho-deriver"]
    manifest = sb.verify(generation)
    assert manifest["fingerprint"] == {"memory.t": [2, "abc"]}
    assert sb.generations(store) == [generation]
    assert docker.running == {"db", "honcho-api", "honcho-deriver"}


def test_a_backup_killed_midway_leaves_no_generation_and_honcho_running(tmp_path):
    docker, lines = FakeDocker(fail={"lina-store: exec db pg_basebackup"}), []
    store = make_store(tmp_path, docker, lines)
    st.ensure_env(store.directory)
    docker.running = {"db", "honcho-api", "honcho-deriver"}
    with pytest.raises(st.StoreError, match="pg_basebackup"):
        sb.backup(store)
    assert sb.generations(store) == []
    assert docker.running == {"db", "honcho-api", "honcho-deriver"}


def test_the_backup_after_a_killed_one_starts_honcho_again(tmp_path):
    docker, lines = FakeDocker(), []
    store = make_store(tmp_path, docker, lines)
    st.ensure_env(store.directory)
    (sb.backups_dir(store) / ".partial-20261101T000000Z").mkdir(parents=True)
    docker.running = {"db"}
    sb.backup(store)
    assert docker.running == {"db", "honcho-api", "honcho-deriver"}
    assert [p.name for p in sb.backups_dir(store).iterdir() if p.name.startswith(".")] == []


def test_backup_needs_a_running_database(tmp_path):
    store = make_store(tmp_path, FakeDocker(), [])
    st.ensure_env(store.directory)
    with pytest.raises(st.StoreError, match="not running"):
        sb.backup(store)


def _one_generation(tmp_path):
    docker, lines = FakeDocker(), []
    store = make_store(tmp_path, docker, lines)
    st.ensure_env(store.directory)
    docker.running = {"db"}
    return docker, store, sb.backup(store)


def test_a_corrupted_generation_is_refused_with_its_cause(tmp_path):
    docker, store, generation = _one_generation(tmp_path)
    (generation / sb.ARCHIVE).write_bytes(b"cluster-bytez")
    with pytest.raises(st.StoreError, match="is corrupted: base.tar.gz has sha256"):
        sb.restore(store, generation.name)
    assert not any("--entrypoint" in s or "run db" in s for s in docker.steps)


def test_a_generation_without_manifest_is_not_offered(tmp_path):
    _docker, store, generation = _one_generation(tmp_path)
    (generation / sb.MANIFEST).unlink()
    with pytest.raises(st.StoreError, match="no backup named"):
        sb.restore(store, generation.name)


def test_restore_switches_to_a_new_volume_and_keeps_the_old_one(tmp_path):
    docker, store, generation = _one_generation(tmp_path)
    previous = sb.restore(store, generation.name)
    current = store.values()["LINA_STORE_VOLUME"]
    assert previous == "lina-store-pgdata" and current != previous and current in docker.volumes
    assert docker.running == {"db", "honcho-api", "honcho-deriver"}


def test_restore_that_does_not_match_the_backup_goes_back_to_the_old_volume(tmp_path):
    docker, store, generation = _one_generation(tmp_path)
    docker.restored_tables = {"memory.t": [1, "abc"]}
    with pytest.raises(st.StoreError, match="differs from .* in memory.t"):
        sb.restore(store, generation.name)
    assert store.values()["LINA_STORE_VOLUME"] == "lina-store-pgdata"


def test_a_rejected_restore_starts_the_honcho_writers_it_stopped_again(tmp_path):
    docker, store, generation = _one_generation(tmp_path)
    docker.running = {"db", "honcho-api", "honcho-deriver"}
    docker.restored_tables = {"memory.t": [1, "abc"]}
    with pytest.raises(st.StoreError, match="differs from"):
        sb.restore(store, generation.name)
    assert docker.running == {"db", "honcho-api", "honcho-deriver"}


def test_upgrade_runs_backup_rehearsal_one_job_and_smoke_test_in_order(tmp_path, honcho_src):
    docker, lines = FakeDocker(volume_exists=True), []
    store = make_store(tmp_path, docker, lines)
    st.ensure_env(store.directory)
    docker.running = {"db", "honcho-api", "honcho-deriver"}
    st.install(store)
    steps = [s for s in docker.steps if "basebackup" in s or "migrate" in s or "down" in s or s.endswith("deriver")]
    assert steps == ["lina-store: stop honcho-api honcho-deriver", "lina-store: exec db pg_basebackup",
                     "lina-store: up honcho-api honcho-deriver", "lina-store-rehearsal: --profile job run honcho-migrate",
                     "lina-store-rehearsal: down", "lina-store: stop honcho-api honcho-deriver",
                     "lina-store: --profile job run honcho-migrate", "lina-store: up honcho-api honcho-deriver"]
    assert [line for line in lines if line.startswith("Upgrade ")][-1].startswith("Upgrade done.")


def test_a_failed_rehearsal_leaves_the_store_and_its_image_alone(tmp_path, honcho_src):
    docker, lines = FakeDocker(volume_exists=True, fail={"lina-store-rehearsal: --profile job"}), []
    docker.images[st.HONCHO_IMAGE] = "sha256:old"
    store = make_store(tmp_path, docker, lines)
    st.ensure_env(store.directory)
    docker.running = {"db", "honcho-api", "honcho-deriver"}
    with pytest.raises(st.StoreError, match="rehearsal failed, so the store was not changed"):
        su.upgrade(store)
    assert "lina-store: --profile job run honcho-migrate" not in docker.steps
    assert "lina-store-rehearsal: down" in docker.steps
    assert docker.steps[-1] == "tag lina-honcho:previous"
    assert docker.running == {"db", "honcho-api", "honcho-deriver"}


def test_restore_after_failed_upgrades_starts_honcho_on_the_image_from_before_them(tmp_path, honcho_src,
                                                                                    monkeypatch):
    stamps = iter(f"20261102T00000{n}Z" for n in range(10))
    monkeypatch.setattr(sb, "stamp", lambda now=None: next(stamps))
    docker, lines = FakeDocker(volume_exists=True, fail={"lina-store: --profile job"}), []
    docker.images[st.HONCHO_IMAGE] = "sha256:old"
    store = make_store(tmp_path, docker, lines)
    st.ensure_env(store.directory)
    docker.running = {"db", "honcho-api", "honcho-deriver"}
    for _attempt in range(2):  # the retry moves lina-honcho:previous off the old image
        with pytest.raises(st.StoreError, match="lina store restore"):
            su.upgrade(store)
    assert "sha256:old" not in (docker.images[st.HONCHO_IMAGE], docker.images[su.PREVIOUS_IMAGE])
    docker.fail.clear()
    sb.restore(store, sb.generations(store)[0].name)
    assert docker.writer_image == "sha256:old"
    assert docker.running == {"db", "honcho-api", "honcho-deriver"}


def test_backups_list_reads_the_manifests(tmp_path, capsys, monkeypatch):
    _docker, store, generation = _one_generation(tmp_path)
    from hermes_cli.subcommands import store as cmd
    cmd._list_backups(store)
    assert capsys.readouterr().out.split()[:4] == [generation.name, str(len(b"cluster-bytes")), "bytes", "1"]
    assert json.loads((generation / sb.MANIFEST).read_text())["created"] == generation.name
