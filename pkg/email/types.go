// Package email defines the public types for transactional email sending.
// The implementation lives in internal/email — this package only exposes
// the interface and data types so the cloud repo can use them via ServerContext.
package email

import "context"

// Mailer sends transactional emails.
type Mailer interface {
	Send(ctx context.Context, to string, subject string, data EmailData) error
}

// EmailData is the generic payload for all transactional emails.
// Templates conditionally render each field based on presence.
type EmailData struct {
	RecipientName string  // optional — used after "Hello"
	Message       string  // main body (plain text; HTML template wraps it)
	Button        *Button // optional CTA button
	Closing       string  // optional closing/footer message
}

// Button represents a call-to-action link rendered as a button in HTML
// and as a plain URL in the text variant.
type Button struct {
	Text string // button label
	URL  string // target URL
}
