from pathlib import Path

import pytest

from tests._fixtures.memory_store import CLUSTER_ENV, ClusterUnavailable, start_cluster


@pytest.fixture
def postgres_without_vector(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    sharedir = tmp_path / "share" / "postgresql"
    (sharedir / "extension").mkdir(parents=True)
    for name in ("initdb", "pg_ctl", "pg_config"):
        binary = tmp_path / name
        binary.write_text(
            '#!/bin/sh\n[ "$1" = "--sharedir" ] && printf "%s\\n" "$TEST_SHAREDIR"\n'
            if name == "pg_config" else "#!/bin/sh\nexit 1\n"
        )
        binary.chmod(0o755)
    monkeypatch.setenv("PATH", str(tmp_path))
    monkeypatch.setenv("TEST_SHAREDIR", str(sharedir))
    monkeypatch.delenv(CLUSTER_ENV, raising=False)
    return sharedir


@pytest.mark.platforms("posix")
def test_missing_vector_reports_selected_postgres_and_sharedir(tmp_path: Path, postgres_without_vector: Path) -> None:
    with pytest.raises(ClusterUnavailable, match="no Postgres for the memory store tests") as error:
        start_cluster()

    assert str(tmp_path / "initdb") in str(error.value)
    assert str(tmp_path / "pg_config") in str(error.value)
    assert str(postgres_without_vector / "extension" / "vector.control") in str(error.value)


@pytest.mark.platforms("posix")
@pytest.mark.usefixtures("postgres_without_vector")
def test_missing_vector_falls_back_to_docker_run(tmp_path: Path) -> None:
    docker = tmp_path / "docker"
    docker.write_text(
        '#!/bin/sh\n'
        'case "$1" in\n'
        '  info) exit 0 ;;\n'
        '  run) echo "test image unavailable" >&2; exit 1 ;;\n'
        '  *) exit 2 ;;\n'
        'esac\n'
    )
    docker.chmod(0o755)

    with pytest.raises(ClusterUnavailable, match="test image unavailable"):
        start_cluster()
