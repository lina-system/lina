"""API admission never blocks the shared owner loop on a held SQLite writer (every HTTP door).

Ordered, not timed: another connection holds the state.db writer until the OWNER LOOP itself lets
it go, after the admission's write is already waiting on it. An admission that wrote on the loop
would park the loop inside BEGIN IMMEDIATE, the release below could never run, and the request
would fail once the store's write patience ran out; a wall-clock gap bound instead flaked on loaded
CI runners (0.66 s against 0.5 s while the write was correctly off the loop).
"""
import asyncio
import sqlite3
import threading

import pytest
from aiohttp import web
from aiohttp.test_utils import TestClient, TestServer

from tests.waiting import async_wait_until

KEY = 'off-loop-admission-key-long-enough'
AUTH = {'Authorization': 'Bearer ' + KEY}

DOORS = [
    ('chat', '/v1/chat/completions', {'messages': [{'role': 'user', 'content': 'hi'}]}, {}, 200),
    ('responses', '/v1/responses', {'input': 'hi'}, {'Idempotency-Key': 'resp-1'}, 200),
    ('runs', '/v1/runs', {'input': 'hi'}, {}, 202),
]


@pytest.mark.asyncio
@pytest.mark.parametrize('name,path,body,headers,expected', DOORS, ids=[d[0] for d in DOORS])
async def test_admission_waits_for_the_writer_off_the_owner_loop(api, owner, monkeypatch, name, path, body,
                                                                headers, expected):
    from gateway.platforms import api_server_runs

    async def handle(event):
        from gateway.session_results import execution_result
        execution_result.get()['result'] = {'final_response': 'ok', 'messages': []}
        return 'ok'

    async def no_execution(*args, **kwargs):
        pass
    owner.runner._handle_message = handle
    monkeypatch.setattr(api_server_runs, '_execute_run', no_execution)
    api._api_key = KEY

    loop_thread = threading.get_ident()
    write_threads = []
    real_execute_write = owner.db._execute_write

    def recording_execute_write(fn, *args, **kwargs):
        write_threads.append(threading.get_ident())
        return real_execute_write(fn, *args, **kwargs)
    monkeypatch.setattr(owner.db, '_execute_write', recording_execute_write)

    app = web.Application()
    app.router.add_post(path, getattr(api, {'chat': '_handle_chat_completions', 'responses': '_handle_responses',
                                            'runs': '_handle_runs'}[name]))
    async with TestClient(TestServer(app)) as client:
        # Another process holds the profile's state.db writer (a long transaction).
        holder = sqlite3.connect(owner.db.db_path, timeout=0, isolation_level=None, check_same_thread=False)
        holder.execute('BEGIN IMMEDIATE')
        try:
            request = asyncio.create_task(client.post(path, json=body, headers={**AUTH, **headers}))
            # Resumes only while the loop runs: the admission's write has started, and the loop is
            # still free to serve other sessions, timers and sockets while that write waits.
            await async_wait_until(lambda: write_threads, timeout=30, what='the admission write to start')
            assert not request.done(), 'the admission must wait for the writer, not skip the commit'
        finally:
            holder.execute('COMMIT')
            holder.close()
        response = await asyncio.wait_for(request, 60)
        assert response.status == expected, await response.text()
    assert loop_thread not in write_threads, 'an admission write ran on the owner loop'
