"""Docker mount records drive live cwd sanitization."""
from tools.environments.docker import DockerEnvironment
import os

def test_mount_records_host_cwd(tmp_path):
    docker = object.__new__(DockerEnvironment)  # _mount_args needs no docker daemon
    docker._persistent = False
    DockerEnvironment._mount_args(docker, [], str(tmp_path), True, "t")
    assert docker.host_cwd == os.path.abspath(str(tmp_path))
    DockerEnvironment._mount_args(docker, [], str(tmp_path / "gone"), True, "t")
    assert docker.host_cwd is None
