"""A2A forward and hosted-room member creation commit their session off the owner loop (N90 siblings):
a held SQLite writer must not stall every session, Stop and socket while either creates."""
import asyncio
import sqlite3
import threading
import time



def _hold(db_path):
    held, release = threading.Event(), threading.Event()

    def hold_writer():  # another process's writer
        other = sqlite3.connect(db_path, timeout=10)
        other.execute('BEGIN IMMEDIATE')
        held.set()
        release.wait(5)
        other.rollback()
        other.close()
    threading.Thread(target=hold_writer, daemon=True).start()
    assert held.wait(5)
    return release


async def _max_gap_while(create, release):
    gaps = []

    async def ticker():
        last = time.monotonic()
        while True:
            await asyncio.sleep(0.02)  # health: allow HX013 -- the ticker's period is the owner-loop stall measurement
            now = time.monotonic()
            gaps.append(now - last)
            last = now
    tick = asyncio.create_task(ticker())
    await asyncio.sleep(0)
    task = asyncio.ensure_future(create())
    try:
        await asyncio.sleep(1.0)  # health: allow HX013 -- the writer hold IS the stall window the ticker measures
        release.set()
        result = await asyncio.wait_for(task, 30)
    finally:
        release.set()
        tick.cancel()
    return max(gaps), result





def test_hosted_member_creation_commits_off_the_owner_loop(hosted_owner):
    from gateway.session_hosted_rpc import HostedRoomAuthorityRPC

    authority, loop, principal, _ = hosted_owner

    def rpc(room):
        return HostedRoomAuthorityRPC(authority, loop, room_id=room, member_id='one', profile='default',
                                      principal=principal, authorize=lambda *a: True)
    coords = {'profile': 'default', 'source': 'bot_room'}
    rpc('warm').create(**coords, title='Group: warm')  # cold imports are not the write under test
    member = rpc('room')
    release = _hold(authority.db.db_path)

    async def probe():
        return await _max_gap_while(lambda: member._dispatch('create', dict(coords, title='Group: room')), release)
    gap, created = asyncio.run_coroutine_threadsafe(probe(), loop).result(40)
    assert created['session_id'] == member.ref.session_id
    assert authority.db.get_session(member.ref.session_id)['title'] == 'Group: room'
    assert gap < 0.5, f'the owner loop stalled {gap:.2f}s behind a held writer'
