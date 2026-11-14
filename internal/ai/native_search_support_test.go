package ai

import (
	"context"
	"strings"
	"testing"
)

type plainClient struct{ Client }

func TestSupportsNativeWebSearch(t *testing.T) {
	cases := []struct {
		name   string
		client Client
		want   bool
	}{
		{"chat completions", &OpenAI{}, false},
		{"responses", &Responses{}, true},
		{"anthropic", &Anthropic{}, true},
		{"gemini official", &Gemini{BaseURL: "https://generativelanguage.googleapis.com"}, true},
		{"gemini relay", &Gemini{BaseURL: "https://gemini-relay.example/v1beta"}, false},
		{"unknown client", plainClient{}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := SupportsNativeWebSearch(tc.client); got != tc.want {
			t.Errorf("%s: %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestAPISupportsNativeWebSearch(t *testing.T) {
	cases := []struct {
		api, base string
		want      bool
	}{
		{"openai-completions", "https://api.example/v1", false},
		{"", "", false},
		{APIResponses, "https://api.example/v1", true},
		{APIMessages, "https://api.example", true},
		{APIGemini, "https://generativelanguage.googleapis.com", true},
		{APIGemini, "https://relay.example", false},
		{APIGoogleGemini, "https://generativelanguage.googleapis.com/v1beta", true},
	}
	for _, tc := range cases {
		if got := APISupportsNativeWebSearch(tc.api, tc.base); got != tc.want {
			t.Errorf("%q %q: %t, want %t", tc.api, tc.base, got, tc.want)
		}
	}
}

func TestChatCompletionsNativeSearchErrorNamesFallback(t *testing.T) {
	c := &OpenAI{BaseURL: "http://127.0.0.1:1/v1"}
	_, _, err := c.Stream(context.Background(), Request{Model: "m", NativeWebSearch: true, Messages: []Message{{Role: "user", Content: "hi"}}}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "configure webSearch") {
		t.Fatalf("err = %v", err)
	}
}
