package imaging

// ProfileSource is where image services come from. The workbench no longer
// owns a service list: the backend derives one profile per chat provider.
type ProfileSource interface {
	Current() ImageProfileSet
}

// ProfileCredentials resolves a profile's API key and extra headers without
// exposing them to the profile document or the wire.
type ProfileCredentials interface {
	Configured(profileID string) bool
	ResolverFor(profileID string) SecretResolver
	HeadersFor(profileID string) map[string]string
}
