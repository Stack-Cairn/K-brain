package backend

import "github.com/Stack-Cairn/K-brain/internal/protocol"

const maxCachedEvents = 512
const maxCachedEventBytes = 4 << 20

// The journal is authoritative; the in-memory tail only accelerates live replay.
func (rt *runtimeSession) appendEventLocked(e protocol.Event) {
	rt.events = append(rt.events, e)
	rt.eventBytes += len(e.Payload) + 256
	for len(rt.events) > maxCachedEvents || rt.eventBytes > maxCachedEventBytes {
		rt.eventBytes -= len(rt.events[0].Payload) + 256
		rt.events[0] = protocol.Event{}
		rt.events = rt.events[1:]
		rt.eventsTruncated = true
	}
}

func (rt *runtimeSession) allEventsLocked() ([]protocol.Event, error) {
	if !rt.eventsTruncated {
		return append([]protocol.Event(nil), rt.events...), nil
	}
	return rt.journalEventsAfter(0, 0)
}

func (rt *runtimeSession) eventsAfterLocked(after int64) ([]protocol.Event, error) {
	if rt.eventsTruncated && (len(rt.events) == 0 || after < rt.events[0].Seq-1) {
		return rt.journalEventsAfter(after, maxCachedEvents)
	}
	var events []protocol.Event
	for _, e := range rt.events {
		if e.Seq > after {
			events = append(events, e)
		}
	}
	return events, nil
}
