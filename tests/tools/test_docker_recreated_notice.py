"""Docker recovery marks recreation for the tool result."""
from tools.environments import docker as docker_env

def test_recovery_marks_recreated(monkeypatch):
    env = docker_env.DockerEnvironment.__new__(docker_env.DockerEnvironment)
    env._container_id = "old"
    env._labels = {}
    env._image = ""
    monkeypatch.setattr(
        docker_env.DockerEnvironment, "_find_reusable_container",
        lambda self, *a: ("newcid", "running"))
    monkeypatch.setattr(docker_env.DockerEnvironment, "init_session", lambda self: None)
    assert env._recreate_container() is True
    assert getattr(env, "_recreated_notice_pending", False) is True
