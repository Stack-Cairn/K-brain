package direct

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProviderOpenAIURLResult(t *testing.T) {
	const outputURL = "https://images.example.invalid/vimg/result"
	for _, format := range []string{"", "png", "jpeg", "webp"} {
		t.Run("format="+format, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"created": 1, "data": []map[string]string{{"url": outputURL, "output_format": format}}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			a := adapter(t, OpenAIID, options(client, "https://provider.example.invalid/v1"))
			r := request(OpenAIID)
			r.Target.Source = "provider"
			r.Target.RemoteID = "gpt-image-2.5-flare"
			result, err := a.Submit(context.Background(), r)
			if err != nil || result.State != "completed" || len(result.Outputs) != 1 {
				t.Fatalf("provider URL result rejected: result=%+v err=%v", result, err)
			}
			mime := ""
			if format != "" {
				mime = "image/" + format
			}
			out := result.Outputs[0]
			if calls != 1 || out.Kind != "url" || out.URL != outputURL || out.MIME != mime {
				t.Fatalf("URL must remain an internal source for the image downloader: calls=%d output=%+v", calls, out)
			}
			if _, err := json.Marshal(result); err == nil {
				t.Fatal("provider URL must not be serialized into a public result")
			}
		})
	}
}

func TestProviderOpenAIURLValidation(t *testing.T) {
	for _, response := range []string{
		`{"data":[{"url":"http://images.example.invalid/result.png"}]}`,
		`{"data":[{"url":"https://user:password@images.example.invalid/result.png"}]}`,
		`{"data":[{"url":"https://images.example.invalid/result.png#fragment"}]}`,
		`{"data":[{"url":"https://images.example.invalid/result.png","b64_json":"AA=="}]}`,
		`{"data":[{"url":"https://images.example.invalid/result.png","output_format":"svg"}]}`,
	} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
		})}
		a := adapter(t, OpenAIID, options(client, "https://provider.example.invalid/v1"))
		r := request(OpenAIID)
		r.Target.Source = "provider"
		result, err := a.Submit(context.Background(), r)
		if err == nil || result.State != "unknown" || len(result.Outputs) != 0 {
			t.Fatalf("invalid provider URL was accepted: result=%+v err=%v", result, err)
		}
	}
}
