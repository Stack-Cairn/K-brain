package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
