package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"

	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	asyncadapter "github.com/Stack-Cairn/K-brain/internal/imaging/adapters/async"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

type imageDependencyFunc func(context.Context, imaging.Scope, []string, []string) (bool, error)

func (f imageDependencyFunc) ImageDependenciesInUse(ctx context.Context, scope imaging.Scope, connections, targets []string) (bool, error) {
	return f(ctx, scope, connections, targets)
}

type imageRuntime struct {
	module      *backend.ImageModule
	scheduler   *imaging.Scheduler
	assets      *imaging.AssetStore
	store       *imaging.Store
	maintenance *imageMaintenance
}

// Local identity is available only on an explicitly loopback-bound listener.
func localImageListener(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func openImageRuntime(ctx context.Context, root string, settings *backend.SettingsStore, allowLocal bool) (_ *imageRuntime, err error) {
	if settings.Snapshot().ImageConfigUnavailable() {
		return nil, errors.New("invalid image configuration; original data preserved")
	}
	store, err := imaging.OpenStore(filepath.Join(root, "imaging"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, store.Close())
		}
	}()
	assets, err := imaging.NewAssetStore(store, filepath.Join(root, "imaging", "assets"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, assets.Close())
		}
	}()
	var service *imaging.Service
	var verification *backend.ImageVerificationManager
	manager, err := backend.NewImageSettingsManager(settings, filepath.Join(root, "imaging", "secrets.json"), backend.ImageSettingsOptions{
		Dependencies: imageDependencyFunc(func(ctx context.Context, scope imaging.Scope, connections, targets []string) (bool, error) {
			if service == nil {
				return false, &imaging.ImageError{Code: imaging.CodeUnavailable}
			}
			return service.ImageDependenciesInUse(ctx, scope, connections, targets)
		}),
	})
	if err != nil {
		return nil, err
	}
	service = imaging.NewService(store, manager, nil, imaging.ServiceOptions{
		AssetSink: assets, InputResolver: assets,
		AdapterResolver: func(ctx context.Context, scope imaging.Scope, target imaging.TargetSnapshot) (imaging.Adapter, error) {
			return resolveVerifiedImageAdapter(ctx, manager, verification, scope, target)
		},
		ConnectionConcurrency: func(scope imaging.Scope, id string) int {
			current, err := manager.ImageSettings(scope)
			if err == nil {
				for _, connection := range current.Connections {
					if connection.ID == id {
						return connection.MaxConcurrent
					}
				}
			}
			return 1
		},
	})
	verification = backend.NewImageVerificationManager(manager, service, assets)
	scheduler := imaging.NewScheduler(service)
	if err = scheduler.Start(ctx); err != nil {
		return nil, err
	}
	return &imageRuntime{
		module:    &backend.ImageModule{Verification: verification, Settings: manager, Store: store, Assets: assets, Jobs: service, AllowLocalImageScope: allowLocal},
		scheduler: scheduler, assets: assets, store: store,
		maintenance: startImageMaintenance(ctx, store, assets),
	}, nil
}

func (r *imageRuntime) Close(ctx context.Context) error {
	if err := r.maintenance.Close(ctx); err != nil {
		return err
	}
	err := r.scheduler.Close(ctx)
	select {
	case <-r.scheduler.Done():
		// A historical scheduler failure must not strand the store's writer lock.
		// Re-read after observing Done: completion can race the Close deadline.
		return errors.Join(r.scheduler.Close(context.Background()), r.assets.Close(), r.store.Close())
	default:
		// A timed-out worker may still be writing: keep its storage open.
		return err
	}
}

func resolveImageAdapter(ctx context.Context, manager *backend.ImageSettingsManager, scope imaging.Scope, target imaging.TargetSnapshot) (imaging.Adapter, error) {
	if target.AdapterVersion != direct.Version {
		return nil, imaging.ErrUnsupported
	}
	connection, err := manager.ResolveImageTargetConnection(ctx, scope, target)
	if err != nil {
		return nil, err
	}
	wantAuth := "bearer"
	if target.Adapter == direct.GoogleID {
		wantAuth = "api_key_header"
	} else if target.Adapter == asyncadapter.JimengID {
		wantAuth = "signed_request"
	}
	if connection.AuthKind != wantAuth {
		connection.HTTPClient.CloseIdleConnections()
		return nil, &imaging.ImageError{Code: imaging.CodeInvalidInput}
	}
	o := direct.Options{HTTPClient: connection.HTTPClient, BaseURL: connection.BaseURL, SecretResolver: connection.SecretResolver, Scope: scope, SecretRef: connection.SecretRefs["apiKey"], CustomHeaders: connection.CustomHeaders}
	var adapter imaging.Adapter
	switch target.Adapter {
	case "rolldek-images":
		if connection.BaseURL != "https://api-cn.rolldek.com/v1" && connection.BaseURL != "https://rolldek.com/v1" {
			return nil, imaging.ErrUnsupported
		}
		o.Profile = "rolldek"
		adapter, err = direct.NewOpenAI(o)
	case direct.OpenAIID:
		o.Profile = direct.ProfileForBaseURL(connection.BaseURL)
		adapter, err = direct.NewOpenAI(o)
	case direct.GoogleID:
		adapter, err = direct.NewGoogle(o)
	case direct.VolcengineID:
		adapter, err = direct.NewVolcengine(o)
	case asyncadapter.RunningHubModelID:
		if connection.BaseURL != "https://www.runninghub.cn" {
			connection.HTTPClient.CloseIdleConnections()
			return nil, imaging.ErrUnsupported
		}
		adapter, err = asyncadapter.NewRunningHubModel(asyncadapter.Options{Connection: connection.ResolvedConnection, Scope: scope, BaseURL: connection.BaseURL, SecretRef: connection.SecretRefs["apiKey"]})
	case asyncadapter.JimengID:
		adapter, err = asyncadapter.NewJimeng(asyncadapter.Options{
			Connection: connection.ResolvedConnection, Scope: scope, BaseURL: connection.BaseURL,
			AccessKeyRef: connection.SecretRefs["accessKey"], SecretKeyRef: connection.SecretRefs["secretKey"],
			SecurityTokenRef: connection.SecretRefs["securityToken"],
		})
	default:
		err = imaging.ErrUnsupported
	}
	if err != nil {
		connection.HTTPClient.CloseIdleConnections()
		return nil, err
	}
	return &imageAdapterClient{Adapter: adapter, client: connection.HTTPClient, useProxy: connection.UseSystemProxy}, nil
}

// Resolvers create connection-scoped transports; release idle sockets after use.
type imageAdapterClient struct {
	imaging.Adapter
	client   *http.Client
	useProxy bool
}

// routed sends output downloads the same way as the connection's own requests.
func (a *imageAdapterClient) routed(result imaging.RemoteResult, err error) (imaging.RemoteResult, error) {
	for i := range result.Outputs {
		result.Outputs[i].UseProxy = a.useProxy
	}
	return result, err
}

func (a *imageAdapterClient) Submit(ctx context.Context, request imaging.ExecuteRequest) (imaging.RemoteResult, error) {
	defer a.client.CloseIdleConnections()
	return a.routed(a.Adapter.Submit(ctx, request))
}
func (a *imageAdapterClient) Poll(ctx context.Context, handle string) (imaging.RemoteResult, error) {
	defer a.client.CloseIdleConnections()
	return a.routed(a.Adapter.Poll(ctx, handle))
}
func (a *imageAdapterClient) Cancel(ctx context.Context, handle string) error {
	defer a.client.CloseIdleConnections()
	return a.Adapter.Cancel(ctx, handle)
}
func (a *imageAdapterClient) Reconcile(ctx context.Context, key string) (imaging.RemoteResult, error) {
	defer a.client.CloseIdleConnections()
	return a.routed(a.Adapter.Reconcile(ctx, key))
}
