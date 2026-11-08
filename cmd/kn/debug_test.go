package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDebugStacksAsksTheBackend(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/debug/stacks" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/data/stacks-20260101T000000.000Z-42.txt"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := debugCLI([]string{"stacks", "--backend", "--url", server.URL, "--token", "tok"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "/data/stacks-20260101T000000.000Z-42.txt" {
		t.Fatalf("printed %q", got)
	}
	if auth != "Bearer tok" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestDebugStacksReportsARefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"stack dumps are only served to loopback clients"}`))
	}))
	defer server.Close()
	err := debugCLI([]string{"stacks", "--backend", "--url", server.URL}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v, want the backend's reason", err)
	}
}

func TestDebugStacksUsage(t *testing.T) {
	for _, args := range [][]string{{"stacks"}, {"stacks", "nope"}, {"stacks", "0"}, {"frobnicate"}, {"stacks", "--backend", "extra"}} {
		if err := debugCLI(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: want a usage error", args)
		}
	}
}
