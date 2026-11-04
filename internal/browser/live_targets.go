package browser

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// ErrLiveTargetInUse means another backend in this process owns the live tab.
var ErrLiveTargetInUse = errors.New("live target is already owned by another browser session")

type liveTargetKey struct {
	endpoint string
	targetID string
}

// Ownership follows target identity across navigation and connection recovery.
// Backends are pointer values; their identity is independent of session names and drivers.
type liveTargetRegistry struct {
	mu     sync.Mutex
	owners map[liveTargetKey]Backend
}

var liveTargets liveTargetRegistry

var browserEndpointID = regexp.MustCompile(`\A/devtools/browser/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\z`)

func liveTargetIdentity(endpoint, targetID string) liveTargetKey {
	// Chrome's browser UUID survives address aliases and query parameters.
	if u, err := url.Parse(endpoint); err == nil && (u.Scheme == "ws" || u.Scheme == "wss") && browserEndpointID.MatchString(u.Path) {
		endpoint = strings.ToLower(u.Path)
	}
	return liveTargetKey{endpoint, targetID}
}

func (r *liveTargetRegistry) claim(endpoint, targetID string, owner Backend) error {
	if owner.Mode() != ModeLive {
		return nil
	}
	key := liveTargetIdentity(endpoint, targetID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if other := r.owners[key]; other != nil && other != owner {
		return fmt.Errorf("%w: %s", ErrLiveTargetInUse, targetID)
	}
	if r.owners == nil {
		r.owners = make(map[liveTargetKey]Backend)
	}
	r.owners[key] = owner
	return nil
}

func (r *liveTargetRegistry) release(endpoint, targetID string, owner Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := liveTargetIdentity(endpoint, targetID)
	if r.owners[key] == owner {
		delete(r.owners, key)
	}
}

func (r *liveTargetRegistry) releaseAll(owner Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, current := range r.owners {
		if current == owner {
			delete(r.owners, key)
		}
	}
}
