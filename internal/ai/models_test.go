package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestModelsUsesConfiguredV1Base(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"model2"}]}`)
	}))
	defer srv.Close()
	models, err := New(srv.URL+"/v1", "key").Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "model2" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestCompatibleModelCatalogURLs(t *testing.T) {
	for _, api := range []string{APIResponses, APIMessages} {
		for _, tc := range []struct {
			name, base, catalog string
			full, override      bool
		}{
			{name: "root relay", catalog: "/v1/models"},
			{name: "root models", catalog: "/models"},
			{name: "versioned", base: "/v1", catalog: "/v1/models"},
			{name: "catalog address", base: "/v1/models", catalog: "/v1/models"},
			{name: "responses address", base: "/v1/responses", catalog: "/v1/models"},
			{name: "messages address", base: "/v1/messages", catalog: "/v1/models"},
			{name: "prefixed relay", base: "/tenant", catalog: "/tenant/v1/models"},
			{name: "escaped relay", base: "/tenant%2Fname", catalog: "/tenant%2Fname/v1/models"},
			{name: "full address", base: "/tenant/v1/responses", catalog: "/tenant/v1/models", full: true},
			{name: "full unversioned completions", base: "/chat/completions", catalog: "/v1/models", full: true},
			{name: "full prefixed completions", base: "/tenant/chat/completions", catalog: "/tenant/v1/models", full: true},
			{name: "explicit address", catalog: "/catalog?region=cn", override: true},
		} {
			if api == APIMessages && (tc.name == "root models" || tc.name == "responses address") {
				continue
			}
			t.Run(api+"/"+tc.name, func(t *testing.T) {
				var paths []string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					paths = append(paths, r.URL.RequestURI())
					if r.URL.RequestURI() != tc.catalog {
						http.NotFound(w, r)
						return
					}
					if r.Header.Get("Authorization") != "Bearer fixture-key" {
						http.Error(w, "missing bearer", http.StatusUnauthorized)
						return
					}
					_, _ = fmt.Fprint(w, `{"data":[{"id":"fixture-model"}]}`)
				}))
				defer srv.Close()
				opts := ClientOptions{API: api, BaseURL: srv.URL + tc.base, APIKey: "fixture-key", IsFullURL: tc.full}
				if tc.override {
					opts.ModelsURL = srv.URL + tc.catalog
				}
				client, err := NewClient(opts)
				if err != nil {
					t.Fatal(err)
				}
				models, err := client.Models(context.Background())
				if err != nil || len(models) != 1 || models[0].ID != "fixture-model" {
					t.Fatalf("models=%+v err=%v paths=%v", models, err, paths)
				}
				if tc.override && len(paths) != 1 {
					t.Fatalf("explicit catalog must not be rewritten: %v", paths)
				}
			})
		}
	}
}

func TestModelCatalogFallbackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		override bool
		fallback bool
		wantErr  bool
	}{
		{name: "not found", status: 404, body: "not found", fallback: true},
		{name: "unsupported route", status: 405, fallback: true},
		{name: "HTML landing page", status: 200, body: "<!DOCTYPE html><title>Gateway</title>", fallback: true},
		{name: "auth failure", status: 401, wantErr: true},
		{name: "forbidden", status: 403, wantErr: true},
		{name: "rate limited", status: 429, wantErr: true},
		{name: "server failure", status: 500, wantErr: true},
		{name: "empty catalog", status: 200, body: `{"data":[]}`},
		{name: "explicit missing catalog", status: 404, override: true, wantErr: true},
		{name: "explicit HTML catalog", status: 200, body: "<html></html>", override: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/v1/models" {
					_, _ = fmt.Fprint(w, `{"data":[{"id":"grok-fixture"}]}`)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			catalog := ""
			if tc.override {
				catalog = srv.URL + "/models"
			}
			models, err := NewWithOptions(srv.URL, "key", false, catalog, nil).Models(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("models=%+v err=%v", models, err)
			}
			wantPaths := []string{"/models"}
			if tc.fallback {
				wantPaths = append(wantPaths, "/v1/models")
				if len(models) != 1 || models[0].ID != "grok-fixture" {
					t.Fatalf("fallback models=%+v", models)
				}
			}
			if !slices.Equal(paths, wantPaths) {
				t.Fatalf("paths=%v want=%v", paths, wantPaths)
			}
		})
	}
}
