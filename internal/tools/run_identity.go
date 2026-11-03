package tools

import "context"

type runIdentityKey struct{}

type RunIdentity struct {
	ConversationID string
	RunID          string
}

func WithRunIdentity(ctx context.Context, identity RunIdentity) context.Context {
	return context.WithValue(ctx, runIdentityKey{}, identity)
}

func RunIdentityFromContext(ctx context.Context) (RunIdentity, bool) {
	identity, ok := ctx.Value(runIdentityKey{}).(RunIdentity)
	if !ok || identity.ConversationID == "" || identity.RunID == "" {
		return RunIdentity{}, false
	}
	return identity, true
}

// sharesConversation reports whether a process or terminal created under owner may
// be used by the run in ctx. A conversation's runtime keeps its processes and
// terminals across runs, so any later run of that conversation may read or stop
// them; other conversations and calls without a run identity may not. The creating
// run stays on the resource for audit.
func sharesConversation(ctx context.Context, owner RunIdentity) bool {
	identity, ok := RunIdentityFromContext(ctx)
	return ok && owner.ConversationID != "" && owner.ConversationID == identity.ConversationID
}
