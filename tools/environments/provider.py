"""Adapter for in-tree environments using the terminal provider registry."""

from agent.terminal_env_provider import TerminalEnvironmentProvider


class EnvironmentProvider(TerminalEnvironmentProvider):
    """Bind an environment's factory and requirements to the existing provider seam."""

    def __init__(self, name, builder, checker, *, supports_streams=False,
                 skip_container_guards=True):
        self._name = name
        self._builder = builder
        self._checker = checker
        self.supports_streams = supports_streams
        self._skip_container_guards = skip_container_guards

    @property
    def skip_container_guards(self) -> bool:
        return self._skip_container_guards

    @property
    def name(self) -> str:
        return self._name

    def is_available(self) -> bool:
        return True

    def check_requirements(self, config) -> bool:
        return self._checker(config)

    def create_environment(self, *, cwd, timeout, task_id="default", image=None,
                           container_config=None, **kwargs):
        return self._builder(cwd=cwd, timeout=timeout, task_id=task_id, image=image,
                             cc=container_config or {}, **kwargs)
