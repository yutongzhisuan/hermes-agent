"""Tests for the hub event bus: replay cursors, CursorOutOfRange, SlowConsumer.

The bus bridges the SQLite event log (durable, globally monotonic `event_id`)
to in-process subscribers. It is the backbone for the Master `WatchTask`
stream: replay from a cursor, then live events; a stale cursor fails with
`CursorOutOfRangeError`; an over-full per-subscriber buffer fails with
`SlowConsumerError`.
"""

import asyncio

import pytest
import pytest_asyncio

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import open_db
from extend.task_relay.hub.event_bus import (
    CursorOutOfRangeError,
    EventBus,
    EventFilter,
    SlowConsumerError,
)


@pytest_asyncio.fixture
async def db(tmp_path):
    # Unclosed aiosqlite connections leave their worker thread running and
    # hang the interpreter at exit — always close.
    conn = await open_db(str(tmp_path / "t.db"))
    yield conn
    await conn.close()


@pytest_asyncio.fixture
async def bus(db):
    return EventBus(db, HubConfig())


async def _take(stream, n):
    """Read n events off a subscription, then close it."""
    events = []
    try:
        for _ in range(n):
            events.append(await stream.__anext__())
    finally:
        await stream.aclose()
    return events


@pytest.mark.asyncio
async def test_publish_persists_first_and_returns_event(bus, db):
    event = await bus.publish(
        callback_topic="topic-a", task_id="t1", kind="STATUS", payload={"s": "running"}
    )
    assert event.event_id >= 1
    # Persisted before publish returned: replay sees it immediately.
    stored = await db.list_events_after("topic-a", 0)
    assert [e.event_id for e in stored] == [event.event_id]
    assert stored[0].payload_json == event.payload_json


@pytest.mark.asyncio
async def test_event_ids_are_globally_monotonic_across_topics(bus):
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={})
    e2 = await bus.publish(callback_topic="b", task_id="t2", kind="STATUS", payload={})
    e3 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={})
    assert e1.event_id < e2.event_id < e3.event_id


@pytest.mark.asyncio
async def test_subscribe_replays_after_cursor(bus):
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 1})
    e2 = await bus.publish(callback_topic="b", task_id="t2", kind="STATUS", payload={"n": 2})
    e3 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={"n": 3})

    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=e1.event_id)
    events = await _take(stream, 1)
    # Only topic-a events with id > cursor; e2 is another topic.
    assert [e.event_id for e in events] == [e3.event_id]


@pytest.mark.asyncio
async def test_subscribe_since_zero_replays_from_oldest_retained(bus):
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 1})
    e2 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={"n": 2})

    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=0)
    events = await _take(stream, 2)
    assert [e.event_id for e in events] == [e1.event_id, e2.event_id]


@pytest.mark.asyncio
async def test_subscribe_filters_by_batch_and_task(bus):
    e1 = await bus.publish(
        callback_topic="a", task_id="t1", batch_id="b1", kind="STATUS", payload={}
    )
    await bus.publish(callback_topic="a", task_id="t2", batch_id="b2", kind="STATUS", payload={})
    e3 = await bus.publish(
        callback_topic="a", task_id="t1", batch_id="b1", kind="RESULT", payload={}
    )

    by_batch = await bus.subscribe(EventFilter(batch_id="b1"), since_event_id=0)
    events = await _take(by_batch, 2)
    assert [e.event_id for e in events] == [e1.event_id, e3.event_id]

    by_task = await bus.subscribe(EventFilter(task_id="t1"), since_event_id=0)
    events = await _take(by_task, 2)
    assert [e.event_id for e in events] == [e1.event_id, e3.event_id]


@pytest.mark.asyncio
async def test_subscribe_receives_live_events_after_replay(bus):
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 1})

    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=0)
    first = await stream.__anext__()
    assert first.event_id == e1.event_id

    # Live: published after the subscription started, delivered on the stream.
    e2 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={"n": 2})
    second = await stream.__anext__()
    assert second.event_id == e2.event_id
    await stream.aclose()


@pytest.mark.asyncio
async def test_live_event_arriving_during_replay_is_not_duplicated(bus):
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 1})
    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=0)
    # Publish while the subscriber may still be mid-replay; ids must arrive
    # exactly once and in order.
    e2 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={"n": 2})
    events = await _take(stream, 2)
    assert [e.event_id for e in events] == [e1.event_id, e2.event_id]


@pytest.mark.asyncio
async def test_cursor_out_of_range(bus, db):
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 1})
    e2 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 2})
    e3 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={"n": 3})
    # Simulate retention pruning: e1/e2 gone, e3 is now the oldest retained.
    await db._conn.execute(
        "DELETE FROM task_events WHERE event_id <= ?", (e2.event_id,)
    )
    await db._conn.commit()

    with pytest.raises(CursorOutOfRangeError) as exc_info:
        await bus.subscribe(EventFilter(topic="a"), since_event_id=e1.event_id)
    err = exc_info.value
    assert err.requested == e1.event_id
    assert err.oldest == e3.event_id
    assert err.newest == e3.event_id


@pytest.mark.asyncio
async def test_cursor_at_oldest_retained_is_in_range(bus, db):
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={})
    e2 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={})
    await db._conn.execute("DELETE FROM task_events WHERE event_id = ?", (e1.event_id,))
    await db._conn.commit()

    # since == oldest retained: nothing missed, stream opens and replays tail.
    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=e2.event_id)
    e3 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={})
    events = await _take(stream, 1)
    assert [e.event_id for e in events] == [e3.event_id]


@pytest.mark.asyncio
async def test_cursor_out_of_range_not_raised_for_empty_filter(bus):
    # No retained events match the filter: nothing to lose, stream opens empty.
    # The cursor comes from real (unrelated) events, as a reconnect's would.
    await bus.publish(callback_topic="other", task_id="t0", kind="STATUS", payload={})
    e2 = await bus.publish(callback_topic="other", task_id="t0", kind="STATUS", payload={})
    stream = await bus.subscribe(EventFilter(topic="nope"), since_event_id=e2.event_id)
    e = await bus.publish(callback_topic="nope", task_id="t1", kind="STATUS", payload={})
    events = await _take(stream, 1)
    assert [ev.event_id for ev in events] == [e.event_id]


@pytest.mark.asyncio
async def test_cursor_out_of_range_when_all_matching_events_pruned(bus, db):
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={})
    e2 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={})
    e3 = await bus.publish(callback_topic="b", task_id="t2", kind="STATUS", payload={})
    # Retention pruned EVERY topic-a event: the filter's oldest is None, but
    # the reconnect cursor still points into pruned global history.
    await db._conn.execute("DELETE FROM task_events WHERE callback_topic = 'a'")
    await db._conn.commit()

    with pytest.raises(CursorOutOfRangeError) as exc_info:
        await bus.subscribe(EventFilter(topic="a"), since_event_id=e2.event_id)
    err = exc_info.value
    assert err.requested == e2.event_id
    assert err.oldest == e3.event_id  # falls back to the global log floor
    assert err.newest == e3.event_id


@pytest.mark.asyncio
async def test_subscribe_with_nonzero_cursor_on_empty_db_is_allowed(bus):
    # Empty table: nothing was ever pruned, so any cursor is in range.
    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=42)
    await stream.aclose()


@pytest.mark.asyncio
async def test_slow_consumer_closes(db):
    bus = EventBus(db, HubConfig(watch_stream_buffer_events=2))
    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=0)

    # Never read: the 3rd matching event overflows the 2-event buffer.
    await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 1})
    await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 2})
    e3 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 3})

    with pytest.raises(SlowConsumerError) as exc_info:
        await stream.__anext__()
    err = exc_info.value
    assert err.delivered == 0  # nothing ever delivered on this stream
    assert err.newest == e3.event_id

    # The stream is closed after the error, not silently dropping events.
    with pytest.raises(StopAsyncIteration):
        await stream.__anext__()


@pytest.mark.asyncio
async def test_slow_consumer_reports_last_delivered_cursor(db):
    bus = EventBus(db, HubConfig(watch_stream_buffer_events=2))
    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=0)

    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 1})
    await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 2})
    first = await stream.__anext__()
    assert first.event_id == e1.event_id

    # Buffer now holds n=2; two more publishes overflow it.
    await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 3})
    e4 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={"n": 4})

    with pytest.raises(SlowConsumerError) as exc_info:
        await stream.__anext__()
    err = exc_info.value
    assert err.delivered == e1.event_id  # Master resubscribes from here
    assert err.newest == e4.event_id


@pytest.mark.asyncio
async def test_non_matching_events_do_not_fill_buffer(db):
    bus = EventBus(db, HubConfig(watch_stream_buffer_events=1))
    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=0)

    # Events on other topics / tasks never touch this subscriber's buffer.
    await bus.publish(callback_topic="b", task_id="t2", kind="STATUS", payload={})
    await bus.publish(callback_topic="b", task_id="t3", kind="STATUS", payload={})
    e = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={})
    got = await stream.__anext__()
    assert got.event_id == e.event_id
    await stream.aclose()


@pytest.mark.asyncio
async def test_aclose_wakes_consumer_blocked_in_anext(bus):
    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=0)
    # No events: the consumer blocks in queue.get() inside __anext__.
    got = asyncio.create_task(stream.__anext__())
    await asyncio.sleep(0.1)  # let the consumer finish replay and block in get()
    assert not got.done()

    await stream.aclose()
    with pytest.raises(StopAsyncIteration):
        await asyncio.wait_for(got, timeout=1)


@pytest.mark.asyncio
async def test_aclose_delivers_buffered_live_events_before_ending(bus):
    stream = await bus.subscribe(EventFilter(topic="a"), since_event_id=0)
    e1 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={})
    first = await stream.__anext__()
    assert first.event_id == e1.event_id

    # Drive the subscription into the live phase: the caught-up replay finds
    # the log empty and the consumer blocks in queue.get().
    pending = asyncio.create_task(stream.__anext__())
    await asyncio.sleep(0.1)
    assert not pending.done()
    e2 = await bus.publish(callback_topic="a", task_id="t1", kind="RESULT", payload={})
    second = await pending
    assert second.event_id == e2.event_id  # delivered live, not via replay

    # Buffered before close: still delivered, then the stream ends.
    e3 = await bus.publish(callback_topic="a", task_id="t1", kind="STATUS", payload={})
    await stream.aclose()
    third = await stream.__anext__()
    assert third.event_id == e3.event_id
    with pytest.raises(StopAsyncIteration):
        await stream.__anext__()


@pytest.mark.asyncio
async def test_event_filter_requires_a_selector():
    with pytest.raises(ValueError):
        EventFilter()
