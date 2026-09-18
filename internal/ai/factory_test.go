package ai

import "testing"

func TestNewClientAdapters(t *testing.T) {
	for _, tc := range []struct {
		api  string
		want any
	}{
		{APIChatCompletions, (*OpenAI)(nil)},
		{APIResponses, (*Responses)(nil)},
		{APIMessages, (*Anthropic)(nil)},
	} {
		client, err := NewClient(ClientOptions{API: tc.api, BaseURL: "https://example.test/v1", APIKey: "key", MaxRetries: 3})
		if err != nil {
			t.Fatalf("%s: %v", tc.api, err)
		}
		switch tc.want.(type) {
		case *OpenAI:
			if _, ok := client.(*OpenAI); !ok {
				t.Fatalf("%s: got %T", tc.api, client)
			}
		case *Responses:
			if _, ok := client.(*Responses); !ok {
				t.Fatalf("%s: got %T", tc.api, client)
			}
		case *Anthropic:
			if _, ok := client.(*Anthropic); !ok {
				t.Fatalf("%s: got %T", tc.api, client)
			}
		}
	}
}

func TestNewClientValidation(t *testing.T) {
	for _, opts := range []ClientOptions{{API: "bad", BaseURL: "https://example.test", APIKey: "key"}, {API: APIResponses, BaseURL: "/v1", APIKey: "key"}, {API: APIResponses, BaseURL: "https://example.test"}} {
		if _, err := NewClient(opts); err == nil {
			t.Fatalf("expected validation error for %+v", opts)
		}
	}
}
