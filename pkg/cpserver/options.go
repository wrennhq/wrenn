package cpserver

// options holds the configuration for Run.
type options struct {
	version    string
	commit     string
	extensions []Extension
}

// Option configures the control plane server.
type Option func(*options)

// WithVersion sets the version and commit strings for logging.
func WithVersion(version, commit string) Option {
	return func(o *options) {
		o.version = version
		o.commit = commit
	}
}

// WithExtensions registers one or more extensions that add routes and
// background workers to the control plane.
func WithExtensions(exts ...Extension) Option {
	return func(o *options) {
		o.extensions = append(o.extensions, exts...)
	}
}
