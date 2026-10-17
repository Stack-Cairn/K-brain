package main

import (
	"net/http"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

var localImageScope = imaging.Scope{OwnerID: "local", BackendID: "local"}

// resolveProfileAdapter builds the adapter for a profile-backed target. The
// credential stays inside the backend: adapters read it through the resolver.
func resolveProfileAdapter(registry imaging.ProfileRegistry, target imaging.TargetSnapshot) (imaging.Adapter, error) {
	if target.AdapterVersion != direct.Version {
		return nil, imaging.ErrUnsupported
	}
	profile, _, ok := imaging.FindProfileTarget(registry.Store.Current(), target.ID)
	if !ok {
		return nil, &imaging.ImageError{Code: imaging.CodeNotFound}
	}
	client := profileHTTPClient(profile)
	o := direct.Options{
		HTTPClient: client, BaseURL: profile.BaseURL, Scope: localImageScope,
		SecretResolver: registry.Secrets.ResolverFor(profile.ID),
		SecretRef:      "apiKey",
		CustomHeaders:  registry.Secrets.HeadersFor(profile.ID),
	}
	var adapter imaging.Adapter
	var err error
	switch target.Adapter {
	case imaging.RollDekAdapterID:
		o.Profile = "rolldek"
		adapter, err = direct.NewOpenAI(o)
	case imaging.OpenAIAdapterID:
		adapter, err = direct.NewOpenAI(o)
	case imaging.GoogleAdapterID:
		adapter, err = direct.NewGoogle(o)
	case imaging.VolcengineAdapterID:
		adapter, err = direct.NewVolcengine(o)
	default:
		err = imaging.ErrUnsupported
	}
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	return &imageAdapterClient{Adapter: adapter, client: client}, nil
}

func profileHTTPClient(profile imaging.ImageProfile) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !profile.APIProxy {
		transport.Proxy = nil
	}
	timeout := 180 * time.Second
	if profile.TimeoutSeconds > 0 {
		timeout = time.Duration(profile.TimeoutSeconds) * time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
