package routing

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestFailoverClientModelNativeSearchFollowsPrimary(t *testing.T) {
	c := &FailoverClient{candidates: []failoverCandidate{{client: &ai.OpenAI{}}, {client: &ai.Responses{}}}}
	for _, model := range []string{"deepseek-flash", "deepseek-chat", "fixture-model"} {
		if got := ai.SupportsNativeWebSearchForModel(c, model); got != (model == "deepseek-flash") {
			t.Fatalf("Chat Completions/%s native search = %t", model, got)
		}
	}
	c.primary = 1
	if !ai.SupportsNativeWebSearchForModel(c, "fixture-model") {
		t.Fatal("responses primary lost native search")
	}
	if ai.SupportsNativeWebSearchForModel(&FailoverClient{}, "deepseek-flash") {
		t.Fatal("empty wrapper gained native search")
	}
}

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
