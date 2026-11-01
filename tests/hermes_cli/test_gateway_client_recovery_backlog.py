"""Recovery RPCs awaited from a client's only event consumer (#134889 CLI-35/CLI-37 review).

``GatewayChatView`` and ACP await ``session.resume`` / ``prompt.receipt`` from the task that drains
``GatewayClient.events``. The owner attaches the new subscription before it answers, so a burst
larger than the client queue can sit ahead of the answer: the reader must keep reading toward it,
and ACP's post-ack recovery must never be dropped when that queue is full. Real ``GatewayClient``
over a fake socket; probes from @dokterdok's review.
"""
import asyncio
import json

import pytest
from websockets.protocol import State


def event(kind, seq=1, sid='s', admission=None):
    return {'jsonrpc': '2.0', 'method': 'event', 'params': {
        'session_id': sid, 'type': kind, 'replay_epoch': 'e', 'seq': seq, 'admission_id': admission,
        'payload': {'text': 'x'}}}


class ResumeWire:
    state = State.OPEN  # GatewayClient.rpc reads the socket state before it sends

    def __init__(self, burst):
        self.burst = burst
        self.frames = asyncio.Queue()
        self.answer_read = asyncio.Event()

    async def send(self, raw):
        req = json.loads(raw)
        assert req['method'] == 'session.resume'
        # An attached owner can send a batch before its resume answer (owner fanout: 16,384
        # frames; the client queue: 4,096).
        for seq in range(2, self.burst + 2):
            self.frames.put_nowait(event('message.delta', seq))
        self.frames.put_nowait({'jsonrpc': '2.0', 'id': req['id'], 'result': {
            'stored_session_id': 's', 'replay_epoch': 'e', 'last_sequence': 1, 'pending': [], 'prompts': []}})

    async def __aiter__(self):
        while True:
            frame = await self.frames.get()
            if 'id' in frame:
                self.answer_read.set()
            yield json.dumps(frame)


@pytest.mark.asyncio
@pytest.mark.parametrize('burst', [4096, 4097, 6000])
async def test_resume_answer_behind_a_burst_stays_reachable(monkeypatch, burst):
    from hermes_cli import gateway_client
    monkeypatch.setattr(gateway_client, 'EVENT_STALL_TIMEOUT', 30)
    wire = ResumeWire(burst)
    async with gateway_client.GatewayClient(wire) as client:
        from hermes_cli.gateway_chat_view import GatewayChatView
        view = GatewayChatView(client, {'stored_session_id': 's'}, quiet=True)
        pump = asyncio.create_task(view.render())
        failure = lambda: view.failure
        answered = asyncio.create_task(wire.answer_read.wait())
        try:
            wire.frames.put_nowait(event('session.replay_gap'))
            # Well under EVENT_STALL_TIMEOUT: a parked reader would not reach the answer in time.
            await asyncio.wait({pump, answered}, timeout=10, return_when=asyncio.FIRST_COMPLETED)
            assert wire.answer_read.is_set(), f'chat: answer unread, queued={client.events.qsize()}'
            assert failure() is None
        finally:
            pump.cancel()
            answered.cancel()
            await asyncio.gather(pump, answered, return_exceptions=True)
