package config

import "testing"

func TestSamplingParamsParse(t *testing.T) {
	var c Config
	err := parseConfigJSONC([]byte(`{
        "providers": {"p": {"baseUrl": "http://x", "models": [
            {"id": "with", "samplingParams": {"temperature": 0.2, "top_p": 0.9}},
            {"id": "without"}
        ]}}
    }`), &c)
	if err != nil {
		t.Fatal(err)
	}
	sp := c.Models["with"].SamplingParams
	if sp == nil || sp.Temperature == nil || *sp.Temperature != 0.2 || sp.TopP == nil || *sp.TopP != 0.9 {
		t.Fatalf("samplingParams did not parse: %+v", sp)
	}
	if c.Models["without"].SamplingParams != nil {
		t.Fatalf("model without samplingParams should have nil, got %+v", c.Models["without"].SamplingParams)
	}
}
