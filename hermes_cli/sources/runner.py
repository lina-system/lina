"""Manual, serialized reads. Only the transport callback can spend a call."""

from __future__ import annotations

from copy import deepcopy
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from time import time

from .defaults import CALL_LIMITS, LIMIT_KEYS, MAIL_QUERY
from .state import ERROR_KINDS, RoomCheckpoint, SourceState, check_spool, locked_state, state_directory, timestamp


@dataclass(frozen=True)
class RunOptions:
    state_dir: Path | None = None
    spool: Path | None = None
    since: datetime | None = None
    max_calls: int | None = None
    dry_run: bool = False
    probe: bool = False


@dataclass
class Result:
    outcome: str
    calls: int = 0
    messages: int = 0
    error: str | None = None


class _LimitReached(Exception):
    """Stop before initiating the next read or accepting another message."""


def _integer(value: object) -> int:
    if type(value) is not int or value < 0:
        raise ValueError("invalid source limit")
    return value


@dataclass
class _Run:
    kind: str
    options: RunOptions
    state: SourceState
    path: Path
    spool: Path | None
    limits: tuple[int, int, int]
    result: Result

    def call(self, tool: str, params: dict, account: str | None = None) -> dict:
        from . import adapters

        adapters.check_tool(self.kind, tool)
        now = time()
        hour, day = self.state.usage(now)
        counts = (self.result.calls, hour, day)
        for count, cap, reason in zip(counts, self.limits, ("run_limit", "hour_limit", "day_limit")):
            if count >= cap:
                raise _LimitReached(reason)
        if self.result.messages >= 100:
            raise _LimitReached("message_limit")
        self.state.calls = [t for t in self.state.calls if t > now - 86400]
        self.state.calls.append(now)
        if self.result.calls == 0:
            self.state.last_attempt = now
        self.state.outcome = "started"
        self.state.save(self.path)
        self.result.calls += 1
        return adapters.invoke(self.kind, tool, params, account=account)

    def emit(self, message: dict) -> None:
        from .spool import append_message

        if self.result.messages >= 100:
            raise _LimitReached("message_limit")
        if self.spool is not None:
            append_message(self.spool, message)
        self.result.messages += 1

    def collect(self, config: dict, since: datetime, until: datetime) -> Result:
        from . import adapters

        try:
            checkpoint = RoomCheckpoint(self.state, self.path if self.spool is not None else None)
            rooms = deepcopy(self.state.rooms)
            if self.options.since is not None:
                for room in rooms.values():
                    if "pending" in room and self.options.since > timestamp(room["pending"]["since"]):
                        room.pop("pending")
            adapters.collect(self.kind, config, since, until, rooms, self.call, self.emit, checkpoint,
                             probe=self.options.probe)
            if self.options.probe and self.result.calls != 1:
                raise ValueError("incomplete probe")
        except _LimitReached as limit:
            self.result.outcome = str(limit)
        except adapters.SourceError as exc:
            if exc.kind == "message_limit":
                self.result.outcome = "message_limit"
            else:
                reason = exc.kind if exc.kind in ERROR_KINDS else ("auth" if exc.auth else "provider_error")
                self.fail(reason, auth=exc.auth)
        except Exception:
            # SDK and malformed-response exceptions may include the raw body. Persist a fixed kind only.
            self.fail("collection_error")
        else:
            now = time()
            if self.options.probe:
                self.state.last_probe_success = now
            elif not self.options.dry_run:
                self.state.last_success = now
            self.state.failures = 0
            self.state.last_error = None
            self.state.error_time = None
        self.state.outcome = self.result.outcome
        self.state.save(self.path)
        return self.result

    def fail(self, reason: str, *, auth: bool = False) -> None:
        self.state.failures += 1
        self.state.last_error = self.result.error = reason
        self.state.error_time = time()
        self.state.stopped = auth or self.state.failures >= 3
        self.result.outcome = "stopped" if self.state.stopped else "error"


def run(kind: str, options: RunOptions) -> Result:
    from hermes_cli.external_messages import configured_spool, source_settings

    config = source_settings(kind)
    if config.get("enabled") is not True:
        return Result("disabled")
    if kind == "mail":
        accounts = config.get("accounts")
        if not isinstance(accounts, list) or not any(isinstance(a, str) and a.strip() for a in accounts):
            return Result("no_accounts")
        query = config.get("query", MAIL_QUERY)
        if not isinstance(query, str) or not query.strip():
            return Result("no_query")
    directory = state_directory(options.state_dir)
    spool = None
    if not options.probe and not options.dry_run:
        spool = options.spool.expanduser() if options.spool is not None else configured_spool()
        if spool is None:
            return Result("no_spool")
        check_spool(spool, directory)
    caps = [min(limit, _integer(config.get(key, limit))) for key, limit in
            zip(LIMIT_KEYS, CALL_LIMITS[kind])]
    if options.max_calls is not None:
        caps[0] = min(caps[0], _integer(options.max_calls))
    if options.probe:
        caps[0] = min(caps[0], 1)
    interval = 60 * max(60, _integer(config.get("min_interval_minutes", 60)))
    with locked_state(directory, kind) as (path, state):
        if state.stopped:
            return Result("stopped", error=state.last_error)
        now = time()
        if state.last_attempt is not None and now - state.last_attempt < interval:
            return Result("not_due")
        until = datetime.fromtimestamp(now, UTC)
        since = until - timedelta(days=2)
        if options.since is not None:
            if options.since.utcoffset() is None or options.since > until:
                raise ValueError("invalid collection window")
            since = max(since, options.since)
        operation = _Run(kind, options, state, path, spool, (caps[0], caps[1], caps[2]), Result("ok"))
        return operation.collect(config, since, until)


def resume(kind: str, directory: Path | None) -> Result:
    with locked_state(state_directory(directory), kind) as (path, state):
        if state.stopped:
            state.stopped = False
            state.failures = 0
            state.outcome = "resumed"
            state.save(path)
        return Result("ready")


def status(kind: str, directory: Path | None) -> dict:
    from hermes_cli.external_messages import source_settings

    state = SourceState.load(state_directory(directory) / f"{kind}.json")
    hour, day = state.usage(time())
    return {
        "enabled": source_settings(kind).get("enabled") is True,
        "stopped": state.stopped, "calls_hour": hour, "calls_day": day,
        "failures": state.failures, "rooms": sum("cursor" in room for room in state.rooms.values()),
        "pending_rooms": sum("pending" in room for room in state.rooms.values()),
        "last_success": state.last_success, "last_probe_success": state.last_probe_success,
        "last_error": state.last_error, "error_time": state.error_time, "outcome": state.outcome,
    }
