package computer

import (
	"fmt"
	"strings"
	"sync"
)

type Policy struct {
	mu sync.Mutex

	sessionAllow map[string]bool
	sessionDeny  map[string]bool

	allow map[string]bool
	deny  map[string]bool

	DefaultDeny bool
}

func NewPolicy(allow, deny []string, defaultDeny bool) *Policy {
	p := &Policy{sessionAllow: map[string]bool{}, sessionDeny: map[string]bool{}, allow: map[string]bool{}, deny: map[string]bool{}, DefaultDeny: defaultDeny}
	for _, a := range allow {
		p.allow[normalize(a)] = true
	}
	for _, d := range deny {
		p.deny[normalize(d)] = true
	}
	return p
}

func normalize(app string) string { return strings.ToLower(strings.TrimSpace(app)) }

func (p *Policy) Check(app string) error {
	n := normalize(app)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deny[n] || p.sessionDeny[n] {
		return fmt.Errorf("computer-use is blocked from using %q by policy (computer.deny)", app)
	}
	if p.allow[n] || p.sessionAllow[n] {
		return nil
	}
	if p.DefaultDeny {
		return &ApprovalNeeded{App: app}
	}
	return nil
}

func (p *Policy) Approve(app string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := normalize(app)
	p.sessionAllow[n] = true
	delete(p.sessionDeny, n)
}

func (p *Policy) Deny(app string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := normalize(app)
	p.sessionDeny[n] = true
	delete(p.sessionAllow, n)
}

func (p *Policy) Summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.allow)+len(p.sessionAllow))
	for a := range p.allow {
		out = append(out, a)
	}
	for a := range p.sessionAllow {
		out = append(out, a+" (session)")
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

type ApprovalNeeded struct{ App string }

func (e *ApprovalNeeded) Error() string {
	return fmt.Sprintf("computer-use needs approval to drive %q — approve in the prompt, or add it to computer.allow in ~/.k-brain/config.json", e.App)
}
