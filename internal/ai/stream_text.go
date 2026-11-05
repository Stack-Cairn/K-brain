package ai

import (
	"strings"
	"unsafe"
)

// streamText appends streamed deltas to a string field without copying what
// was already received. `field += delta` copies the whole text on every delta,
// which makes a long reply or a large tool argument quadratic in its length.
//
// append returns the new value for the field. While the field still holds the
// string returned last time, the delta is written to the same buffer; if the
// field was set to something else in between, accumulation restarts from it.
type streamText struct {
	b    strings.Builder
	last string
}

func (t *streamText) append(cur, delta string) string {
	if len(cur) != len(t.last) || unsafe.StringData(cur) != unsafe.StringData(t.last) {
		t.b.Reset()
		t.b.WriteString(cur)
	}
	t.b.WriteString(delta)
	t.last = t.b.String()
	return t.last
}

// streamTextFor returns the accumulator for key, creating it on first use.
func streamTextFor[K comparable](m map[K]*streamText, key K) *streamText {
	t := m[key]
	if t == nil {
		t = &streamText{}
		m[key] = t
	}
	return t
}
