package apperr

import "net/http"

// registry maps code → Def so errors received over RPC (see connect.go) and
// codes from extensions resolve back to their catalog entry.
var registry = map[string]Def{}

func register(d Def) Def {
	registry[d.Code] = d
	return d
}

// Generic codes. Prefer a domain-specific Def when the client can act on the
// distinction; use these with Msg for one-off cases.
var (
	Internal = register(Def{
		Code:    "internal_error",
		Status:  http.StatusInternalServerError,
		Message: "Something went wrong on our end. Try again; if it keeps failing, contact support with the request ID.",
	})
	InvalidRequest = register(Def{
		Code:    "invalid_request",
		Status:  http.StatusBadRequest,
		Message: "The request is invalid.",
	})
	ValidationFailed = register(Def{
		Code:    "validation_failed",
		Status:  http.StatusBadRequest,
		Message: "One or more fields are invalid.",
	})
	Unauthorized = register(Def{
		Code:    "unauthorized",
		Status:  http.StatusUnauthorized,
		Message: "Authentication required.",
	})
	Forbidden = register(Def{
		Code:    "forbidden",
		Status:  http.StatusForbidden,
		Message: "You don't have permission to do that.",
	})
	NotFound = register(Def{
		Code:    "not_found",
		Status:  http.StatusNotFound,
		Message: "The requested resource was not found.",
	})
	Conflict = register(Def{
		Code:    "conflict",
		Status:  http.StatusConflict,
		Message: "The request conflicts with the current state of the resource.",
	})
	PayloadTooLarge = register(Def{
		Code:    "payload_too_large",
		Status:  http.StatusRequestEntityTooLarge,
		Message: "The request payload is too large.",
	})
	RateLimited = register(Def{
		Code:      "rate_limited",
		Status:    http.StatusTooManyRequests,
		Retryable: true,
		Message:   "Too many requests. Slow down and try again.",
	})
	Timeout = register(Def{
		Code:      "timeout",
		Status:    http.StatusGatewayTimeout,
		Retryable: true,
		Message:   "The operation timed out. Try again.",
	})
	Unavailable = register(Def{
		Code:      "service_unavailable",
		Status:    http.StatusServiceUnavailable,
		Retryable: true,
		Message:   "The service is temporarily unavailable. Try again shortly.",
	})
	NotImplemented = register(Def{
		Code:    "not_implemented",
		Status:  http.StatusNotImplemented,
		Message: "This operation is not supported.",
	})
)

// Auth and account codes.
var (
	AuthInvalidCredentials = register(Def{
		Code:    "auth_invalid_credentials",
		Status:  http.StatusUnauthorized,
		Message: "Invalid email or password.",
	})
	AuthSessionRequired = register(Def{
		Code:    "auth_session_required",
		Status:  http.StatusUnauthorized,
		Message: "A valid session or API key is required.",
	})
	AuthInvalidAPIKey = register(Def{
		Code:    "auth_invalid_api_key",
		Status:  http.StatusUnauthorized,
		Message: "Invalid API key.",
	})
	AuthCSRF = register(Def{
		Code:    "auth_csrf_failed",
		Status:  http.StatusForbidden,
		Message: "CSRF token missing or invalid. Refresh the page and try again.",
	})
	AuthAccountNotActivated = register(Def{
		Code:    "auth_account_not_activated",
		Status:  http.StatusForbidden,
		Message: "Check your email and activate your account before signing in.",
	})
	AuthAccountDisabled = register(Def{
		Code:    "auth_account_disabled",
		Status:  http.StatusForbidden,
		Message: "Your account has been deactivated — contact your administrator to regain access.",
	})
	AuthEmailTaken = register(Def{
		Code:    "auth_email_taken",
		Status:  http.StatusConflict,
		Message: "An account with this email already exists.",
	})
	AuthSignupCooldown = register(Def{
		Code:    "auth_signup_cooldown",
		Status:  http.StatusConflict,
		Message: "A signup for this email is already pending. Check your inbox or try again later.",
	})
	AuthTokenInvalid = register(Def{
		Code:    "auth_token_invalid",
		Status:  http.StatusBadRequest,
		Message: "This link is invalid or has expired.",
	})
	AuthAlreadyActivated = register(Def{
		Code:    "auth_already_activated",
		Status:  http.StatusConflict,
		Message: "This account has already been activated.",
	})
)

// Sandbox lifecycle and infrastructure codes.
var (
	SandboxNotFound = register(Def{
		Code:    "sandbox_not_found",
		Status:  http.StatusNotFound,
		Message: "Sandbox not found.",
	})
	SandboxNotRunning = register(Def{
		Code:    "sandbox_not_running",
		Status:  http.StatusConflict,
		Message: "Sandbox is not running.",
	})
	SandboxNotPaused = register(Def{
		Code:    "sandbox_not_paused",
		Status:  http.StatusConflict,
		Message: "Sandbox is not paused.",
	})
	SandboxUnresponsive = register(Def{
		Code:      "sandbox_unresponsive",
		Status:    http.StatusBadGateway,
		Retryable: true,
		Message:   "The sandbox is not responding. Try again shortly.",
	})
	HostUnreachable = register(Def{
		Code:      "host_unreachable",
		Status:    http.StatusBadGateway,
		Retryable: true,
		Message:   "Could not reach the sandbox's host. Try again shortly.",
	})
	HostDraining = register(Def{
		Code:      "host_draining",
		Status:    http.StatusServiceUnavailable,
		Retryable: true,
		Message:   "The host is shutting down and not accepting new work. Try again shortly.",
	})
	CapacityUnavailable = register(Def{
		Code:      "capacity_unavailable",
		Status:    http.StatusServiceUnavailable,
		Retryable: true,
		Message:   "No hosts currently have capacity for this request. Try again shortly.",
	})
	TemplateNotFound = register(Def{
		Code:    "template_not_found",
		Status:  http.StatusNotFound,
		Message: "Template not found.",
	})
	TemplateProtected = register(Def{
		Code:    "template_protected",
		Status:  http.StatusForbidden,
		Message: "System templates cannot be modified or deleted.",
	})
	SnapshotNotFound = register(Def{
		Code:    "snapshot_not_found",
		Status:  http.StatusNotFound,
		Message: "Snapshot not found.",
	})
	BuildNotFound = register(Def{
		Code:    "build_not_found",
		Status:  http.StatusNotFound,
		Message: "Build not found.",
	})
	HostNotFound = register(Def{
		Code:    "host_not_found",
		Status:  http.StatusNotFound,
		Message: "Host not found.",
	})
	HostHasActiveSandboxes = register(Def{
		Code:    "has_active_sandboxes",
		Status:  http.StatusConflict,
		Message: "This host has active sandboxes. Pass ?force=true to destroy them and delete the host.",
	})
	TeamNotFound = register(Def{
		Code:    "team_not_found",
		Status:  http.StatusNotFound,
		Message: "Team not found.",
	})
	UserNotFound = register(Def{
		Code:    "user_not_found",
		Status:  http.StatusNotFound,
		Message: "User not found.",
	})
	VolumeNotFound = register(Def{
		Code:    "volume_not_found",
		Status:  http.StatusNotFound,
		Message: "Volume not found.",
	})
	VolumeInUse = register(Def{
		Code:    "volume_in_use",
		Status:  http.StatusConflict,
		Message: "Volume is attached to a capsule. Destroy the capsule before deleting the volume.",
	})
	VolumeHostMismatch = register(Def{
		Code:    "volume_host_mismatch",
		Status:  http.StatusConflict,
		Message: "This volume is pinned to a different host than the requested capsule can run on.",
	})
	VolumesAttached = register(Def{
		Code:    "volumes_attached",
		Status:  http.StatusConflict,
		Message: "Detach all volumes before creating a template from this capsule.",
	})
	VolumeNameTaken = register(Def{
		Code:    "volume_name_taken",
		Status:  http.StatusConflict,
		Message: "A volume with this name already exists.",
	})
)

// Lookup returns the catalog Def for a code, or false if unregistered.
// Extensions can add their own codes with Register.
func Lookup(code string) (Def, bool) {
	d, ok := registry[code]
	return d, ok
}

// Register adds a Def to the catalog registry so errors carrying its code
// resolve to it when received over RPC. Intended for extensions; OSS codes
// are registered at package init. Registering an existing code overwrites it.
func Register(d Def) Def { return register(d) }
