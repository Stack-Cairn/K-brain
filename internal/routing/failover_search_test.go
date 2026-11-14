package routing

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestFailoverClientNativeWebSearchFollowsPrimary(t *testing.T) {
	c := &FailoverClient{candidates: []failoverCandidate{{client: &ai.OpenAI{}}, {client: &ai.Responses{}}}}
	if c.SupportsNativeWebSearch() {
		t.Fatal("chat completions primary reported native search")
	}
	c.primary = 1
	if !c.SupportsNativeWebSearch() {
		t.Fatal("responses primary did not report native search")
	}
	if (&FailoverClient{}).SupportsNativeWebSearch() {
		t.Fatal("empty failover client reported native search")
	}
	if !ai.SupportsNativeWebSearch(c) {
		t.Fatal("failover client does not implement the supporter interface")
	}
}
