package oauth

import "context"

// UserProfile is the normalized user info returned by an OAuth provider.
type UserProfile struct {
	ProviderID string
	Email      string
	Name       string
}

// Provider abstracts an OAuth 2.0 identity provider.
type Provider interface {
	// Name returns the provider identifier (e.g. "github", "google").
	Name() string
	// AuthCodeURL returns the URL to redirect the user to for authorization.
	AuthCodeURL(state string) string
	// Exchange trades an authorization code for a user profile.
	Exchange(ctx context.Context, code string) (UserProfile, error)
}

// Registry maps provider names to Provider implementations.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	r.providers[p.Name()] = p
}

// Get looks up a provider by name.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}
