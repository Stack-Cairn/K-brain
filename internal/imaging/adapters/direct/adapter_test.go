package direct

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

type secretFunc func(context.Context, imaging.Scope, string) (string, error)

func (f secretFunc) ResolveSecret(c context.Context, s imaging.Scope, r string) (string, error) {
	return f(c, s, r)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func options(client *http.Client, base string) Options {
	return Options{HTTPClient: client, BaseURL: base, Scope: imaging.Scope{OwnerID: "synthetic-owner", BackendID: "synthetic-backend"}, SecretRef: "synthetic-ref", SecretResolver: secretFunc(func(_ context.Context, s imaging.Scope, ref string) (string, error) {
		if s.OwnerID != "synthetic-owner" || s.BackendID != "synthetic-backend" || ref != "synthetic-ref" {
			return "", errors.New("wrong scope")
		}
		return "synthetic-key", nil
	})}
}
func adapter(t *testing.T, id string, o Options) *Adapter {
	t.Helper()
	var a *Adapter
	var err error
	switch id {
	case OpenAIID:
		a, err = NewOpenAI(o)
	case GoogleID:
		a, err = NewGoogle(o)
	case VolcengineID:
		a, err = NewVolcengine(o)
	}
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func request(id string) imaging.ExecuteRequest {
	model := map[string]string{OpenAIID: "gpt-image-2", GoogleID: "gemini-nano-banana-2.1", VolcengineID: "doubao-seedream-5-0-pro-260628"}[id]
	return imaging.ExecuteRequest{Target: imaging.TargetSnapshot{Adapter: id, AdapterVersion: Version, Kind: "model", RemoteID: model}, Request: imaging.CreateJobRequest{Operation: imaging.OperationGenerate, Count: 1, Prompt: "A synthetic ceramic bird"}, SubmissionKey: "local-only"}
}
func syntheticPNG(t *testing.T, n int, shade byte, mask bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: shade, G: 80, B: 90, A: 255})
		}
	}
	if mask {
		img.SetNRGBA(0, 0, color.NRGBA{})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func responseJSON(id string, raw []byte) string {
	b64 := base64.StdEncoding.EncodeToString(raw)
	if id == GoogleID {
		return `{"candidates":[{"content":{"role":"model","parts":[{"text":"Synthetic result"},{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]},"finishReason":"STOP"}]}`
	}
	return `{"data":[{"b64_json":"` + b64 + `","output_format":"png"}]}`
}
func addInput(r *imaging.ExecuteRequest, id, role string, raw []byte, closed *atomic.Int32) {
	r.Request.Inputs = append(r.Request.Inputs, imaging.InputAsset{AssetID: id, Role: role})
	r.Inputs = append(r.Inputs, imaging.ResolvedAsset{AssetID: id, MIME: "image/png", Bytes: int64(len(raw)), Open: func() (io.ReadCloser, error) { return &countCloser{Reader: bytes.NewReader(raw), closed: closed}, nil }})
}

type countCloser struct {
	io.Reader
	closed *atomic.Int32
}

func (c *countCloser) Close() error {
	if c.closed != nil {
		c.closed.Add(1)
	}
	return nil
}
func assertError(t *testing.T, res imaging.RemoteResult, err error, code string, delivery imaging.Delivery) {
	t.Helper()
	var ae *imaging.AdapterError
	if !errors.As(err, &ae) || ae.Code != code || ae.Delivery != delivery {
		t.Fatalf("error=%v result=%+v", err, res)
	}
	if delivery == imaging.DeliveryUncertain && res.State != "unknown" {
		t.Fatalf("uncertain state=%s", res.State)
	}
	if ae.Cause != nil || errors.Unwrap(ae) != nil {
		t.Fatal("retained raw cause")
	}
	raw, e := json.Marshal(ae)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"SECRET_SENTINEL", "http://", "https://", "synthetic-key", "A synthetic ceramic bird"} {
		if strings.Contains(string(raw)+err.Error(), s) {
			t.Fatal("leaked sensitive information")
		}
	}
}

func TestContractGenerate(t *testing.T) {
	for _, id := range []string{OpenAIID, GoogleID, VolcengineID} {
		t.Run(id, func(t *testing.T) {
			raw := syntheticPNG(t, 16, 30, false)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("method/content-type")
				}
				if r.Header.Get("Idempotency-Key") != "" || r.URL.RawQuery != "" {
					t.Error("invented idempotency or query auth")
				}
				path := "/v1/images/generations"
				auth := "Bearer synthetic-key"
				if id == GoogleID {
					path = "/v1beta/models/gemini-nano-banana-2.1:generateContent"
					auth = ""
					if r.Header.Get("x-goog-api-key") != "synthetic-key" {
						t.Error("key header")
					}
				}
				if id == VolcengineID {
					path = "/api/v3/images/generations"
				}
				if r.URL.Path != path || r.Header.Get("Authorization") != auth {
					t.Errorf("path/auth mismatch %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if id == VolcengineID && body["output_format"] != "png" {
					t.Error("single-image PNG verification profile must explicitly request PNG")
				}
				var expected map[string]any
				name := map[string]string{OpenAIID: "openai-generate.json", GoogleID: "google-generate.json", VolcengineID: "ark-generate.json"}[id]
				fixture, err := os.ReadFile("testdata/" + name)
				if err != nil || json.Unmarshal(fixture, &expected) != nil {
					t.Error("invalid request fixture")
				}
				if !reflect.DeepEqual(body, expected) {
					t.Errorf("unexpected native schema: %#v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, responseJSON(id, raw))
			}))
			defer srv.Close()
			prefix := map[string]string{OpenAIID: "/v1", GoogleID: "/v1beta", VolcengineID: "/api/v3"}[id]
			a := adapter(t, id, options(srv.Client(), srv.URL+prefix))
			res, err := a.Submit(context.Background(), request(id))
			if err != nil || res.State != "completed" || len(res.Outputs) != 1 || !bytes.Equal(res.Outputs[0].Data, raw) || res.Outputs[0].MIME != "image/png" || res.Usage != nil || calls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", res, err, calls)
			}
		})
	}
}

func TestContractReferencesPreserveOrder(t *testing.T) {
	for _, id := range []string{OpenAIID, GoogleID, VolcengineID} {
		t.Run(id, func(t *testing.T) {
			first, second := syntheticPNG(t, 16, 20, false), syntheticPNG(t, 16, 80, false)
			var closed atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b map[string]json.RawMessage
				json.NewDecoder(r.Body).Decode(&b)
				var got []string
				switch id {
				case OpenAIID:
					if r.URL.Path != "/images/edits" {
						t.Error("edit route")
					}
					var refs []imageRef
					json.Unmarshal(b["images"], &refs)
					for _, ref := range refs {
						got = append(got, strings.SplitN(ref.URL, ",", 2)[1])
					}
				case GoogleID:
					var c []googleContent
					json.Unmarshal(b["contents"], &c)
					for _, p := range c[0].Parts[1:] {
						got = append(got, p.Inline.Data)
					}
				case VolcengineID:
					var refs []string
					json.Unmarshal(b["image"], &refs)
					for _, ref := range refs {
						got = append(got, strings.SplitN(ref, ",", 2)[1])
					}
				}
				want := []string{base64.StdEncoding.EncodeToString(first), base64.StdEncoding.EncodeToString(second)}
				if !reflect.DeepEqual(got, want) {
					t.Error("reference order changed")
				}
				io.WriteString(w, responseJSON(id, first))
			}))
			defer srv.Close()
			a := adapter(t, id, options(srv.Client(), srv.URL))
			r := request(id)
			r.Request.Operation = imaging.OperationReference
			addInput(&r, "first", "reference", first, &closed)
			addInput(&r, "second", "reference", second, &closed)
			if _, err := a.Submit(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if closed.Load() != 2 {
				t.Fatal("input readers not closed")
			}
		})
	}
}

func TestContractOpenAIMask(t *testing.T) {
	src, mask := syntheticPNG(t, 16, 40, false), syntheticPNG(t, 16, 40, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b openAIRequest
		json.NewDecoder(r.Body).Decode(&b)
		if r.URL.Path != "/images/edits" || len(b.Images) != 1 || b.Mask == nil || b.Images[0].URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(src) || b.Mask.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(mask) {
			t.Error("mask schema/first source")
		}
		io.WriteString(w, responseJSON(OpenAIID, src))
	}))
	defer srv.Close()
	a := adapter(t, OpenAIID, options(srv.Client(), srv.URL))
	r := request(OpenAIID)
	r.Request.Operation = imaging.OperationMask
	r.Request.ParentAssetID = "source"
	var closed atomic.Int32
	addInput(&r, "source", "source", src, &closed)
	addInput(&r, "mask", "mask", mask, &closed)
	if _, err := a.Submit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 2 {
		t.Fatal("not closed")
	}
	for _, tc := range []struct {
		name   string
		change func(*imaging.ExecuteRequest)
	}{
		{"opaque", func(r *imaging.ExecuteRequest) {
			r.Inputs = nil
			r.Request.Inputs = nil
			addInput(r, "source", "source", src, nil)
			addInput(r, "mask", "mask", src, nil)
		}},
		{"wrong dimensions", func(r *imaging.ExecuteRequest) {
			r.Inputs = nil
			r.Request.Inputs = nil
			addInput(r, "source", "source", src, nil)
			addInput(r, "mask", "mask", syntheticPNG(t, 17, 40, true), nil)
		}},
		{"wrong parent", func(r *imaging.ExecuteRequest) { r.Request.ParentAssetID = "other" }},
		{"reference before source", func(r *imaging.ExecuteRequest) {
			r.Inputs = nil
			r.Request.Inputs = nil
			addInput(r, "ref", "reference", src, nil)
			addInput(r, "source", "source", src, nil)
			addInput(r, "mask", "mask", mask, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := r
			tc.change(&copy)
			res, err := a.Submit(context.Background(), copy)
			assertError(t, res, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		})
	}
}

func TestContractRejectBeforeHTTP(t *testing.T) {
	for _, id := range []string{OpenAIID, GoogleID, VolcengineID} {
		t.Run(id, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })}
			a := adapter(t, id, options(client, "https://example.invalid"))
			for _, tc := range []struct {
				name   string
				change func(*imaging.ExecuteRequest)
			}{
				{"missing remote", func(r *imaging.ExecuteRequest) { r.Target.RemoteID = "" }},
				{"malformed model", func(r *imaging.ExecuteRequest) { r.Target.RemoteID = "../models/x" }},
				{"batch", func(r *imaging.ExecuteRequest) { r.Request.Count = 2 }},
				{"unknown parameter", func(r *imaging.ExecuteRequest) { r.Request.Parameters = map[string]any{"stream": true} }},
				{"prompt", func(r *imaging.ExecuteRequest) { r.Request.Prompt = strings.Repeat("x", 32001) }},
				{"continuation", func(r *imaging.ExecuteRequest) { r.Continuation = []byte("opaque") }},
				{"unordered resolved", func(r *imaging.ExecuteRequest) {
					r.Request.Operation = imaging.OperationReference
					r.Request.Inputs = []imaging.InputAsset{{AssetID: "one", Role: "reference"}}
					r.Inputs = []imaging.ResolvedAsset{{AssetID: "other"}}
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := request(id)
					tc.change(&r)
					res, err := a.Submit(context.Background(), r)
					var e *imaging.AdapterError
					if !errors.As(err, &e) || e.Delivery != imaging.DeliveryNotSent || res.State != "failed" {
						t.Fatalf("%+v %v", res, err)
					}
				})
			}
			if calls != 0 {
				t.Fatal("invalid input submitted")
			}
			if _, err := a.Poll(context.Background(), "id"); !errors.Is(err, imaging.ErrUnsupported) {
				t.Fatal(err)
			}
			if err := a.Cancel(context.Background(), "id"); !errors.Is(err, imaging.ErrUnsupported) {
				t.Fatal(err)
			}
			if _, err := a.Reconcile(context.Background(), "key"); !errors.Is(err, imaging.ErrUnsupported) {
				t.Fatal(err)
			}
			if a.Profile().LiveVerified || a.Profile().Capabilities.SupportsContinuation || a.Profile().Capabilities.SupportsCancel || a.Profile().Capabilities.SupportsReconcile {
				t.Fatal("invented capability")
			}
			if id == GoogleID && a.Profile().ContractComplete {
				t.Fatal("Google contract incomplete")
			}
		})
	}
}

func TestContractErrorsAndNoRetry(t *testing.T) {
	for _, id := range []string{OpenAIID, GoogleID, VolcengineID} {
		t.Run(id, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				status   int
				body     string
				code     string
				delivery imaging.Delivery
			}{
				{"bad input", 400, `{"error":{"code":"invalid","message":"SECRET_SENTINEL"}}`, imaging.CodeInvalidInput, imaging.DeliveryRejected},
				{"auth", 401, `SECRET_SENTINEL`, imaging.CodeAuthRequired, imaging.DeliveryRejected},
				{"forbidden", 403, `SECRET_SENTINEL`, imaging.CodeForbidden, imaging.DeliveryRejected},
				{"conflict", 409, `{"error":{"code":"conflict"}}`, imaging.CodeInvalidInput, imaging.DeliveryRejected},
				{"quota", 429, `{"error":{"code":"quota","status":"RESOURCE_EXHAUSTED","message":"SECRET_SENTINEL"}}`, imaging.CodeRateLimited, imaging.DeliveryRejected},
				{"gateway rate", 429, `SECRET_SENTINEL`, imaging.CodeResultUnknown, imaging.DeliveryUncertain},
				{"server", 500, `{"error":{"code":"error"}}`, imaging.CodeResultUnknown, imaging.DeliveryUncertain},
				{"timeout", 408, `SECRET_SENTINEL`, imaging.CodeResultUnknown, imaging.DeliveryUncertain},
				{"cancelled", 499, `SECRET_SENTINEL`, imaging.CodeResultUnknown, imaging.DeliveryUncertain},
				{"malformed", 200, `{"data":[`, imaging.CodeResultUnknown, imaging.DeliveryUncertain},
			} {
				t.Run(tc.name, func(t *testing.T) {
					calls := 0
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						w.WriteHeader(tc.status)
						io.WriteString(w, tc.body)
					}))
					defer srv.Close()
					a := adapter(t, id, options(srv.Client(), srv.URL))
					res, err := a.Submit(context.Background(), request(id))
					assertError(t, res, err, tc.code, tc.delivery)
					if calls != 1 {
						t.Fatal("retried")
					}
				})
			}
		})
	}
}

func TestContractOutputValidation(t *testing.T) {
	raw := syntheticPNG(t, 16, 50, false)
	valid := base64.StdEncoding.EncodeToString(raw)
	for _, tc := range []struct{ name, id, body, state string }{
		{"empty", OpenAIID, `{"data":[]}`, "failed"},
		{"text", GoogleID, `{"candidates":[{"content":{"role":"model","parts":[{"text":"no image"}]},"finishReason":"STOP"}]}`, "failed"},
		{"blocked", GoogleID, `{"promptFeedback":{"blockReason":"SAFETY"}}`, "failed"},
		{"thought only", GoogleID, `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"` + valid + `"}}]},"finishReason":"STOP"}]}`, "failed"},
		{"unknown finish", GoogleID, strings.Replace(responseJSON(GoogleID, raw), "STOP", "NEW_ENUM", 1), "unknown"},
		{"bad base64", OpenAIID, `{"data":[{"b64_json":"a!??"}]}`, "unknown"},
		{"not an image", VolcengineID, `{"data":[{"b64_json":"aGVsbG8="}]}`, "unknown"},
		{"mime mismatch", GoogleID, strings.Replace(responseJSON(GoogleID, raw), "image/png", "image/jpeg", 1), "unknown"},
		{"newline", OpenAIID, `{"data":[{"b64_json":"` + valid[:8] + `\n` + valid[8:] + `"}]}`, "unknown"},
		{"truncated image", OpenAIID, responseJSON(OpenAIID, raw[:len(raw)/2]), "unknown"},
		{"legacy URL", OpenAIID, `{"data":[{"url":"https://example.invalid/a.png"}]}`, "unknown"},
		{"url credentials", VolcengineID, `{"data":[{"url":"https://user:SECRET_SENTINEL@example.invalid/a.png"}]}`, "unknown"},
		{"non https", VolcengineID, `{"data":[{"url":"file:///SECRET_SENTINEL"}]}`, "unknown"},
		{"top error", VolcengineID, `{"error":{"code":"InputTextSensitiveContentDetected","message":"SECRET_SENTINEL"}}`, "failed"},
		{"entry error", VolcengineID, `{"data":[{"error":{"code":"error"}}]}`, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) }))
			defer srv.Close()
			a := adapter(t, tc.id, options(srv.Client(), srv.URL))
			res, err := a.Submit(context.Background(), request(tc.id))
			if res.State != tc.state || len(res.Outputs) != 0 || res.Usage != nil {
				t.Fatalf("%+v %v", res, err)
			}
			if tc.state == "unknown" {
				assertError(t, res, err, imaging.CodeResultUnknown, imaging.DeliveryUncertain)
			} else if err != nil || res.ErrorCode != imaging.CodeNoImageOutput {
				t.Fatal("no image was not terminal failure")
			}
		})
	}
}

func TestContractGoogleAllFinalImagesAndUnknownUsage(t *testing.T) {
	raw := syntheticPNG(t, 16, 50, false)
	b64 := base64.StdEncoding.EncodeToString(raw)
	body := `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"ignored-thought"}},{"text":"explanation","thoughtSignature":"opaque"},{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"},"thoughtSignature":"opaque"},{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":42}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer srv.Close()
	a := adapter(t, GoogleID, options(srv.Client(), srv.URL))
	res, err := a.Submit(context.Background(), request(GoogleID))
	if err != nil || res.State != "completed" || len(res.Outputs) != 2 || res.Usage != nil {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestContractURLNeverDownloaded(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"data":[{"url":"https://example.invalid/private?signature=SECRET_SENTINEL","output_format":"png"}],"usage":{"generated_images":1,"output_tokens":42}}`)
	}))
	defer srv.Close()
	a := adapter(t, VolcengineID, options(srv.Client(), srv.URL))
	r := request(VolcengineID)
	r.Request.Parameters = map[string]any{"response_format": "url"}
	res, err := a.Submit(context.Background(), r)
	if err != nil || calls != 1 || len(res.Outputs) != 1 || res.Outputs[0].Kind != "url" || res.Usage != nil {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := json.Marshal(res); err == nil {
		t.Fatal("private result serialized")
	}
	if _, err := json.Marshal(res.Outputs[0]); err == nil {
		t.Fatal("private URL serialized")
	}
}

func TestContractLimitsAndTransport(t *testing.T) {
	raw := syntheticPNG(t, 16, 40, false)
	for _, tc := range []struct {
		name   string
		limits Limits
		body   string
	}{
		{"body bound", Limits{MaxResponseBytes: 20}, responseJSON(OpenAIID, raw)},
		{"decoded byte bound", Limits{MaxImageBytes: int64(len(raw) - 1)}, responseJSON(OpenAIID, raw)},
		{"pixel bound", Limits{MaxPixels: 255}, responseJSON(OpenAIID, raw)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) }))
			defer srv.Close()
			o := options(srv.Client(), srv.URL)
			o.Limits = tc.limits
			a := adapter(t, OpenAIID, o)
			res, err := a.Submit(context.Background(), request(OpenAIID))
			assertError(t, res, err, imaging.CodeResultUnknown, imaging.DeliveryUncertain)
		})
	}
	t.Run("request bound", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
		defer srv.Close()
		o := options(srv.Client(), srv.URL)
		o.Limits.MaxRequestBytes = 10
		a := adapter(t, OpenAIID, o)
		res, err := a.Submit(context.Background(), request(OpenAIID))
		assertError(t, res, err, imaging.CodeTooLarge, imaging.DeliveryNotSent)
		if calls != 0 {
			t.Fatal("sent oversized request")
		}
	})
	t.Run("dropped response", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			io.Copy(io.Discard, r.Body)
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			c.Close()
		}))
		defer srv.Close()
		a := adapter(t, OpenAIID, options(srv.Client(), srv.URL))
		res, err := a.Submit(context.Background(), request(OpenAIID))
		assertError(t, res, err, imaging.CodeResultUnknown, imaging.DeliveryUncertain)
		if calls.Load() != 1 {
			t.Fatal("replayed paid submit")
		}
	})
	t.Run("transport cause", func(t *testing.T) {
		calls := 0
		o := options(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.GetBody != nil {
				t.Error("replayable body")
			}
			return nil, fmt.Errorf("SECRET_SENTINEL https://secret.invalid/?auth=secret")
		})}, "https://example.invalid")
		a := adapter(t, OpenAIID, o)
		res, err := a.Submit(context.Background(), request(OpenAIID))
		assertError(t, res, err, imaging.CodeResultUnknown, imaging.DeliveryUncertain)
		if calls != 1 {
			t.Fatal("retry")
		}
	})
	t.Run("pre cancelled", func(t *testing.T) {
		a := adapter(t, OpenAIID, options(&http.Client{}, "https://example.invalid"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := a.Submit(ctx, request(OpenAIID))
		assertError(t, res, err, imaging.CodeUnavailable, imaging.DeliveryNotSent)
	})
	t.Run("redirect", func(t *testing.T) {
		targetCalls := 0
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
		defer target.Close()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(307)
		}))
		defer srv.Close()
		client := srv.Client()
		a := adapter(t, GoogleID, options(client, srv.URL))
		res, err := a.Submit(context.Background(), request(GoogleID))
		assertError(t, res, err, imaging.CodeResultUnknown, imaging.DeliveryUncertain)
		if targetCalls != 0 || client.CheckRedirect != nil {
			t.Fatal("redirect followed or injected client mutated")
		}
	})
}

func TestContractFixtures(t *testing.T) {
	// Fixtures are authored schema examples with synthetic text, never receipts.
	for _, name := range []string{"openai-generate.json", "google-generate.json", "ark-generate.json"} {
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if json.Unmarshal(raw, &v) != nil {
			t.Fatal("bad fixture")
		}
	}
}

func TestContractRollDekExplicitBase64Profile(t *testing.T) {
	raw := syntheticPNG(t, 16, 30, false)
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://rolldek.com/v1/images/generations" {
			t.Error("wrong provider endpoint")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["response_format"] != "b64_json" || body["n"] != float64(1) {
			t.Error("RollDek must explicitly request single base64 output")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responseJSON(OpenAIID, raw))), Header: http.Header{}}, nil
	})}
	o := options(client, "https://rolldek.com/v1")
	o.Profile = "rolldek"
	a, err := NewOpenAI(o)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Submit(context.Background(), request(OpenAIID))
	if err != nil || len(res.Outputs) != 1 {
		t.Fatalf("result %v", err)
	}
	changed := request(OpenAIID)
	changed.Request.Parameters = map[string]any{"stream": true}
	if _, err := a.Submit(context.Background(), changed); err == nil {
		t.Fatal("unknown provider parameter accepted")
	}
	if calls != 1 {
		t.Fatal("unsupported input reached upstream")
	}
	o.BaseURL = "https://api.openai.com/v1"
	if _, err := NewOpenAI(o); err == nil {
		t.Fatal("RollDek profile accepted on unrelated provider")
	}
}

func TestContractRollDekHTTPSAlias(t *testing.T) {
	raw := syntheticPNG(t, 16, 30, false)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api-cn.rolldek.com/v1/images/generations" {
			t.Fatal("wrong endpoint")
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["model"] != "gpt-image-2.5" || b["response_format"] != "b64_json" || b["n"] != float64(1) {
			t.Fatal("wrong alias or format")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responseJSON(OpenAIID, raw))), Header: http.Header{}}, nil
	})}
	o := options(client, "https://api-cn.rolldek.com/v1")
	o.Profile = "rolldek"
	a, err := NewOpenAI(o)
	if err != nil {
		t.Fatal(err)
	}
	r := request(OpenAIID)
	r.Target.Adapter = "rolldek-images"
	r.Target.RemoteID = "gpt-image-2.5"
	if _, err := a.Submit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	o.BaseURL = "http://api-cn.rolldek.com/v1"
	if _, err := NewOpenAI(o); err == nil {
		t.Fatal("RollDek authenticated requests must use HTTPS")
	}
	o.Profile = "rolldek"
	o.BaseURL = "http://other.example/v1"
	if _, err := NewOpenAI(o); err == nil {
		t.Fatal("HTTP exception leaked to another host")
	}
}

func TestContractRollDekURLResultDoesNotChangeOfficialOpenAI(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://cdn.example.com/result.png"}]}`))}, nil
	})}
	o := options(client, "https://api-cn.rolldek.com/v1")
	o.Profile = "rolldek"
	a, err := NewOpenAI(o)
	if err != nil {
		t.Fatal(err)
	}
	r := request(OpenAIID)
	r.Target.Adapter = "rolldek-images"
	r.Target.RemoteID = "gpt-image-2.5"
	result, err := a.Submit(context.Background(), r)
	if err != nil || len(result.Outputs) != 1 || result.Outputs[0].Kind != "url" {
		t.Fatalf("RollDek URL rejected: %v", err)
	}
	official := adapter(t, OpenAIID, options(client, "https://api.openai.com/v1"))
	result, err = official.Submit(context.Background(), request(OpenAIID))
	if err == nil || result.State != "unknown" {
		t.Fatal("RollDek URL behavior leaked into official OpenAI profile")
	}
}
