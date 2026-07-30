"""In-process event bus bridged to the SQLite event log.

Backbone for the Master `WatchTask` stream (design spec §WatchTask Replay):

- ``publish`` persists the event FIRST (the DB's AUTOINCREMENT assigns the
  globally monotonic ``event_id``), then wakes live subscribers. No per-topic
  counters — a filtered view is an increasing subsequence of the global log.
- ``subscribe`` replays events with ``event_id > since_event_id`` for the
  filter, then streams live events.
- A ``since_event_id`` older than the oldest retained event for the filter
  fails fast with :class:`CursorOutOfRangeError` — never a silent restart
  from the oldest retained event, which would look like "nothing missed".
- Each subscription has a bounded buffer (``watch_stream_buffer_events``).
  Overflow closes the stream with :class:`SlowConsumerError` carrying the
  last delivered cursor — the only allowed backpressure action; the hub
  never blocks producers on a slow consumer and never drops events silently.
"""

import asyncio
import collections
from dataclasses import dataclass
from typing import AsyncIterator

from extend.task_relay.hub.config import HubConfig
from extend.task_relay.hub.db import Database
from extend.task_relay.hub.models import TaskEvent

# Replay page size; paging keeps a huge backlog from being read at once.
_REPLAY_PAGE = 256


class CursorOutOfRangeError(Exception):
    """The requested cursor predates the oldest retained event for the filter.

    Maps to gRPC FAILED_PRECONDITION + CursorOutOfRange at the service layer.
    """

    def __init__(self, requested: int, oldest: int, newest: int):
        self.requested = requested
        self.oldest = oldest
        self.newest = newest
        super().__init__(
            f"since_event_id {requested} is older than the oldest retained "
            f"event {oldest} (newest: {newest}); reconcile via GetTaskResult / "
            f"ListTasks, then resubscribe with since_event_id={newest}"
        )


class SlowConsumerError(Exception):
    """A subscriber's bounded buffer overflowed; the stream is closed.

    Maps to gRPC RESOURCE_EXHAUSTED + SlowConsumer at the service layer.
    """

    def __init__(self, delivered: int, newest: int):
        self.delivered = delivered
        self.newest = newest
        super().__init__(
            f"watch buffer overflowed after delivering event {delivered} "
            f"(newest: {newest}); reconcile, then resubscribe with "
            f"since_event_id={delivered}"
        )


@dataclass(frozen=True)
class EventFilter:
    """WatchTask `oneof filter`; set fields match conjunctively."""

    topic: str | None = None
    batch_id: str | None = None
    task_id: str | None = None

    def __post_init__(self) -> None:
        if self.topic is None and self.batch_id is None and self.task_id is None:
            raise ValueError("EventFilter needs at least one of topic/batch_id/task_id")

    def matches(self, event: TaskEvent) -> bool:
        if self.topic is not None and event.callback_topic != self.topic:
            return False
        if self.batch_id is not None and event.batch_id != self.batch_id:
            return False
        if self.task_id is not None and event.task_id != self.task_id:
            return False
        return True


class _Subscription:
    """One open watch: replay-from-cursor, then live events via a bounded queue."""

    def __init__(
        self,
        bus: "EventBus",
        filter: EventFilter,
        since_event_id: int,
        buffer_size: int,
    ):
        self._bus = bus
        self._filter = filter
        self._queue: asyncio.Queue[TaskEvent] = asyncio.Queue(maxsize=buffer_size)
        self._overflow_newest: int | None = None
        # Cursor of the last event handed to the consumer (replayed or live).
        self._delivered = since_event_id
        self._replaying = True
        self._page: collections.deque[TaskEvent] = collections.deque()
        self._closed = False

    # -- producer side (called by EventBus.publish, never blocks) -----------

    def offer(self, event: TaskEvent) -> None:
        if self._closed:
            return
        if self._overflow_newest is not None:
            # Already overflowing: keep the tail current, buffer stays full.
            self._overflow_newest = max(self._overflow_newest, event.event_id)
            return
        if not self._filter.matches(event):
            return
        try:
            self._queue.put_nowait(event)
        except asyncio.QueueFull:
            self._overflow_newest = event.event_id

    # -- consumer side ------------------------------------------------------

    def __aiter__(self) -> "_Subscription":
        return self

    async def __anext__(self) -> TaskEvent:
        while True:
            self._raise_if_overflow()
            if self._closed:
                raise StopAsyncIteration
            if self._replaying:
                if not self._page:
                    events = await self._bus._db.list_events_for_filter(
                        topic=self._filter.topic,
                        batch_id=self._filter.batch_id,
                        task_id=self._filter.task_id,
                        after_event_id=self._delivered,
                        limit=_REPLAY_PAGE,
                    )
                    if not events:
                        # Caught up with the log. Live events published during
                        # replay are already queued; ids <= delivered skipped.
                        self._replaying = False
                        continue
                    self._page.extend(events)
                event = self._page.popleft()
                self._delivered = event.event_id
                return event
            else:
                try:
                    event = self._queue.get_nowait()
                except asyncio.QueueEmpty:
                    self._raise_if_overflow()
                    event = await self._queue.get()
                # The queue may hold events replay already delivered.
                if event.event_id <= self._delivered:
                    continue
                self._raise_if_overflow()
                self._delivered = event.event_id
                return event

    def _raise_if_overflow(self) -> None:
        if self._overflow_newest is None:
            return
        # Close the stream: buffered-but-undelivered events are part of the
        # missed gap the Master reconciles after resubscribing.
        newest = max(self._overflow_newest, self._bus._newest_event_id)
        delivered = self._delivered
        self._overflow_newest = None
        self._closed = True
        self._bus._drop(self)
        raise SlowConsumerError(delivered, newest)

    async def aclose(self) -> None:
        self._closed = True
        self._bus._drop(self)


class EventBus:
    """Persist-first pub/sub over the hub's global event log."""

    def __init__(self, db: Database, config: HubConfig):
        self._db = db
        self._buffer_size = config.watch_stream_buffer_events
        self._subscribers: set[_Subscription] = set()
        self._newest_event_id = 0

    async def publish(
        self,
        *,
        callback_topic: str,
        task_id: str | None,
        kind: str,
        payload: dict | None = None,
        batch_id: str | None = None,
        event_at: float | None = None,
    ) -> TaskEvent:
        """Append to the log, THEN notify subscribers with the assigned id."""
        event = await self._db.append_event(
            callback_topic=callback_topic,
            task_id=task_id,
            kind=kind,
            payload=payload,
            batch_id=batch_id,
            event_at=event_at,
        )
        self._newest_event_id = max(self._newest_event_id, event.event_id)
        for sub in list(self._subscribers):
            sub.offer(event)
        return event

    async def subscribe(
        self, filter: EventFilter, since_event_id: int = 0
    ) -> AsyncIterator[TaskEvent]:
        """Replay `event_id > since_event_id` for the filter, then stream live.

        `since_event_id = 0` replays from the oldest retained event. A cursor
        older than the oldest retained event for the filter raises
        CursorOutOfRangeError instead of silently restarting.
        """
        oldest = await self._db.oldest_event_id_for_filter(
            topic=filter.topic, batch_id=filter.batch_id, task_id=filter.task_id
        )
        if since_event_id > 0 and oldest is not None and since_event_id < oldest:
            stored_newest = await self._db.newest_event_id()
            newest = max(self._newest_event_id, stored_newest or 0, oldest)
            raise CursorOutOfRangeError(since_event_id, oldest, newest)
        # Register before any replay so live events published during replay
        # are buffered (and deduplicated by cursor on delivery).
        sub = _Subscription(self, filter, since_event_id, self._buffer_size)
        self._subscribers.add(sub)
        return sub

    def _drop(self, sub: _Subscription) -> None:
        self._subscribers.discard(sub)
