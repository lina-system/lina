from pathlib import Path

import pytest

from tests._fixtures.memory_store import CLUSTER_ENV, ClusterUnavailable, start_cluster


@pytest.mark.platforms("posix")
def test_missing_vector_reports_selected_postgres_and_sharedir(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
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

    with pytest.raises(ClusterUnavailable) as error:
        start_cluster()

    assert str(tmp_path / "pg_config") in str(error.value)
    assert str(sharedir) in str(error.value)
    assert "vector.control" in str(error.value)
