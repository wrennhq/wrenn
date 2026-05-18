package api

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/internal/email"
	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/oauth"
	authsession "git.omukk.dev/wrenn/wrenn/pkg/auth/session"
	"git.omukk.dev/wrenn/wrenn/pkg/channels"
	"git.omukk.dev/wrenn/wrenn/pkg/cpextension"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/events"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/scheduler"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

//go:embed openapi.yaml
var openapiYAML []byte

// Server is the control plane HTTP server.
type Server struct {
	router     chi.Router
	BuildSvc   *service.BuildService
	SSERelay   *SSERelay
	SessionSvc *authsession.Service
	version    string
}

// New constructs the chi router and registers all routes. The jwtSecret is
// still used to sign host-agent JWTs (long-lived, for the wrenn-agent → cp
// trust path) and to HMAC OAuth state/link cookies; user authentication has
// migrated to opaque session cookies backed by the session service.
func New(
	ctx context.Context,
	queries *db.Queries,
	pool *lifecycle.HostClientPool,
	sched scheduler.HostScheduler,
	pgPool *pgxpool.Pool,
	rdb *redis.Client,
	jwtSecret []byte,
	oauthRegistry *oauth.Registry,
	oauthRedirectURL string,
	ca *auth.CA,
	al *audit.AuditLogger,
	eventPub *channels.Publisher,
	channelSvc *channels.Service,
	mailer email.Mailer,
	extensions []cpextension.Extension,
	sctx cpextension.ServerContext,
	monitor *HostMonitor,
	version string,
) *Server {
	r := chi.NewRouter()
	r.Use(requestLogger())

	// Apply extension middleware before routes so it wraps all OSS routes.
	for _, ext := range extensions {
		if mp, ok := ext.(cpextension.MiddlewareProvider); ok {
			r.Use(mp.Middlewares(sctx)...)
		}
	}

	// Session service backs cookie-based browser auth. The cpserver wires it
	// through ServerContext so cloud-repo extensions can share the same
	// instance; fall back to constructing one here if the host program
	// instantiates the API directly (tests, ad-hoc tooling).
	sessionSvc := sctx.Sessions
	if sessionSvc == nil {
		sessionSvc = authsession.NewService(queries, rdb)
	}

	// Shared service layer.
	sandboxSvc := &service.SandboxService{DB: queries, Pool: pool, Scheduler: sched}
	sandboxSvc.PublishEvent = func(ctx context.Context, event service.SandboxStateEvent) {
		if evt, ok := serviceEventToCanonical(event); ok {
			eventPub.Publish(ctx, evt)
		}
	}
	apiKeySvc := &service.APIKeyService{DB: queries}
	templateSvc := &service.TemplateService{DB: queries}
	hostSvc := &service.HostService{DB: queries, Redis: rdb, JWT: jwtSecret, Pool: pool, CA: ca}
	teamSvc := &service.TeamService{DB: queries, Pool: pgPool, HostPool: pool}
	userSvc := &service.UserService{DB: queries, SandboxSvc: sandboxSvc}
	auditSvc := &service.AuditService{DB: queries}
	statsSvc := &service.StatsService{DB: queries, Pool: pgPool}
	usageSvc := &service.UsageService{DB: queries}
	buildSvc := &service.BuildService{DB: queries, Redis: rdb, Pool: pool, Scheduler: sched}

	limitsProvider := collectLimitsProvider(extensions)
	usageProvider := collectUsageProvider(extensions, queries)
	sandbox := newSandboxHandler(sandboxSvc, al, limitsProvider, usageProvider)
	exec := newExecHandler(queries, pool)
	execStream := newExecStreamHandler(queries, pool)
	files := newFilesHandler(queries, pool)
	filesStream := newFilesStreamHandler(queries, pool)
	fsH := newFSHandler(queries, pool)
	snapshots := newSnapshotHandler(templateSvc, sandboxSvc, queries, pool, al)
	authHooks := collectAuthHooks(extensions)
	authH := newAuthHandler(queries, pgPool, sessionSvc, mailer, rdb, oauthRedirectURL, authHooks)
	oauthH := newOAuthHandler(queries, pgPool, jwtSecret, sessionSvc, oauthRegistry, oauthRedirectURL, authHooks)
	apiKeys := newAPIKeyHandler(apiKeySvc, al)
	hostH := newHostHandler(hostSvc, queries, al, monitor)
	teamH := newTeamHandler(teamSvc, al, mailer, sessionSvc)
	usersH := newUsersHandler(queries, userSvc, al, sessionSvc)
	auditH := newAuditHandler(auditSvc)
	statsH := newStatsHandler(statsSvc)
	usageH := newUsageHandler(usageSvc)
	metricsH := newSandboxMetricsHandler(queries, pool)
	buildH := newBuildHandler(buildSvc, queries, pool, al)
	channelH := newChannelHandler(channelSvc, al)
	ptyH := newPtyHandler(queries, pool)
	processH := newProcessHandler(queries, pool)
	adminCapsules := newAdminCapsuleHandler(sandboxSvc, queries, pool, al)
	sandboxEvtH := newSandboxEventHandler(queries, eventPub)
	meH := newMeHandler(queries, pgPool, rdb, jwtSecret, sessionSvc, mailer, oauthRegistry, oauthRedirectURL, teamSvc, authHooks)
	sessionsH := newSessionsHandler(sessionSvc)

	// SSE real-time event streaming.
	sseBroker := NewSSEBroker()
	sseRelay := NewSSERelay(rdb, queries, sseBroker)
	sseH := newSSEHandler(sseBroker)

	// Health check.
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":%q}`, version)
	})

	// OpenAPI spec and docs.
	r.Get("/openapi.yaml", serveOpenAPI)
	r.Get("/docs", serveDocs)

	// Unauthenticated auth endpoints. CSRF is not required here — there is
	// no session cookie yet to be forged.
	r.Post("/v1/auth/signup", authH.Signup)
	r.Post("/v1/auth/login", authH.Login)
	r.Post("/v1/auth/activate", authH.Activate)
	r.Get("/auth/oauth/{provider}", oauthH.Redirect)
	r.Get("/auth/oauth/{provider}/callback", oauthH.Callback)

	// Unauthenticated: password reset request and confirmation.
	r.Post("/v1/me/password/reset", meH.RequestPasswordReset)
	r.Post("/v1/me/password/reset/confirm", meH.ConfirmPasswordReset)

	csrf := requireCSRF()

	// Session-authenticated: logout endpoints (must be inside the session
	// group so the handler sees the current SID via AuthContext).
	r.Group(func(r chi.Router) {
		r.Use(requireSession(queries, sessionSvc))
		r.Use(csrf)
		r.Post("/v1/auth/logout", authH.Logout)
		r.Post("/v1/auth/logout-all", authH.LogoutAll)
		r.Post("/v1/auth/switch-team", authH.SwitchTeam)
	})

	// Session-authenticated: self-service account management.
	r.Route("/v1/me", func(r chi.Router) {
		r.Use(requireSession(queries, sessionSvc))
		r.Use(csrf)
		r.Get("/", meH.GetMe)
		r.Patch("/", meH.UpdateName)
		r.Post("/password", meH.ChangePassword)
		r.Get("/providers/{provider}/connect", meH.ConnectProvider)
		r.Delete("/providers/{provider}", meH.DisconnectProvider)
		r.Delete("/", meH.DeleteAccount)
		r.Get("/sessions", sessionsH.List)
		r.Delete("/sessions/{id}", sessionsH.Delete)
	})

	// Session-authenticated: API key management.
	r.Route("/v1/api-keys", func(r chi.Router) {
		r.Use(requireSession(queries, sessionSvc))
		r.Use(csrf)
		r.Post("/", apiKeys.Create)
		r.Get("/", apiKeys.List)
		r.Delete("/{id}", apiKeys.Delete)
	})

	// Session-authenticated: team management.
	r.Route("/v1/teams", func(r chi.Router) {
		r.Use(requireSession(queries, sessionSvc))
		r.Use(csrf)
		r.Get("/", teamH.List)
		r.Post("/", teamH.Create)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", teamH.Get)
			r.Patch("/", teamH.Rename)
			r.Delete("/", teamH.Delete)
			r.Get("/members", teamH.ListMembers)
			r.Post("/members", teamH.AddMember)
			r.Patch("/members/{uid}", teamH.UpdateMemberRole)
			r.Delete("/members/{uid}", teamH.RemoveMember)
			r.Post("/leave", teamH.Leave)
		})
	})

	// Session-authenticated: user search (for add-member UI).
	r.With(requireSession(queries, sessionSvc), csrf).Get("/v1/users/search", usersH.Search)

	// Capsule lifecycle: API key (SDK) or session cookie (browser).
	r.Route("/v1/capsules", func(r chi.Router) {
		// Auth-required routes.
		r.Group(func(r chi.Router) {
			r.Use(requireSessionOrAPIKey(queries, sessionSvc))
			r.Use(csrf)
			r.Post("/", sandbox.Create)
			r.Get("/", sandbox.List)
			r.Get("/stats", statsH.GetStats)
			r.Get("/usage", usageH.GetUsage)
		})

		r.Route("/{id}", func(r chi.Router) {
			// Auth-required non-WS routes.
			r.Group(func(r chi.Router) {
				r.Use(requireSessionOrAPIKey(queries, sessionSvc))
				r.Use(csrf)
				r.Get("/", sandbox.Get)
				r.Delete("/", sandbox.Destroy)
				r.Post("/exec", exec.Exec)
				r.Post("/ping", sandbox.Ping)
				r.Post("/pause", sandbox.Pause)
				r.Post("/resume", sandbox.Resume)
				r.Post("/files/write", files.Upload)
				r.Post("/files/read", files.Download)
				r.Post("/files/stream/write", filesStream.StreamUpload)
				r.Post("/files/stream/read", filesStream.StreamDownload)
				r.Post("/files/list", fsH.ListDir)
				r.Post("/files/mkdir", fsH.MakeDir)
				r.Post("/files/remove", fsH.Remove)
				r.Get("/metrics", metricsH.GetMetrics)
				r.Get("/processes", processH.ListProcesses)
				r.Delete("/processes/{selector}", processH.KillProcess)
			})

			// WebSocket endpoints — middleware injects auth context from
			// cookie (browser) or X-API-Key (SDK) before upgrade. CSRF is
			// not applicable to GET upgrades.
			r.Group(func(r chi.Router) {
				r.Use(requireSessionOrAPIKey(queries, sessionSvc))
				r.Get("/exec/stream", execStream.ExecStream)
				r.Get("/pty", ptyH.PtySession)
				r.Get("/processes/{selector}/stream", processH.ConnectProcess)
			})
		})
	})

	// Snapshot / template management: API key (SDK) or session (browser).
	r.Route("/v1/snapshots", func(r chi.Router) {
		r.Use(requireSessionOrAPIKey(queries, sessionSvc))
		r.Use(csrf)
		r.Post("/", snapshots.Create)
		r.Get("/", snapshots.List)
		r.Delete("/{name}", snapshots.Delete)
	})

	// Host management.
	r.Route("/v1/hosts", func(r chi.Router) {
		// Unauthenticated: one-time registration token.
		r.Post("/register", hostH.Register)

		// Unauthenticated: refresh token exchange.
		r.Post("/auth/refresh", hostH.RefreshToken)

		// Host-token-authenticated: heartbeat and lifecycle callbacks.
		r.With(requireHostToken(jwtSecret)).Post("/{id}/heartbeat", hostH.Heartbeat)
		r.With(requireHostToken(jwtSecret)).Post("/sandbox-events", sandboxEvtH.Handle)

		// Session-authenticated: host CRUD and tags.
		r.Group(func(r chi.Router) {
			r.Use(requireSession(queries, sessionSvc))
			r.Use(csrf)
			r.Post("/", hostH.Create)
			r.Get("/", hostH.List)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", hostH.Get)
				r.Delete("/", hostH.Delete)
				r.Get("/delete-preview", hostH.DeletePreview)
				r.Post("/token", hostH.RegenerateToken)
				r.Get("/tags", hostH.ListTags)
				r.Post("/tags", hostH.AddTag)
				r.Delete("/tags/{tag}", hostH.RemoveTag)
			})
		})
	})

	// Session-authenticated: notification channels.
	r.Route("/v1/channels", func(r chi.Router) {
		r.Use(requireSession(queries, sessionSvc))
		r.Use(csrf)
		r.Post("/", channelH.Create)
		r.Get("/", channelH.List)
		r.Post("/test", channelH.Test)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", channelH.Get)
			r.Patch("/", channelH.Update)
			r.Delete("/", channelH.Delete)
			r.Put("/config", channelH.RotateConfig)
		})
	})

	// SSE event stream: browser sends wrenn_sid cookie on EventSource
	// automatically; SDKs may set X-API-Key. Ticket-based auth is gone.
	r.With(requireSessionOrAPIKey(queries, sessionSvc)).Get("/v1/events/stream", sseH.Stream)

	// Session-authenticated: audit log.
	r.With(requireSession(queries, sessionSvc), csrf).Get("/v1/audit-logs", auditH.List)

	// Platform admin routes — session + DB-validated admin status.
	r.Route("/v1/admin", func(r chi.Router) {
		// Admin SSE event stream (sees all teams).
		r.With(requireSession(queries, sessionSvc), requireAdmin(queries), injectPlatformTeam()).Get("/events/stream", sseH.AdminStream)

		// Auth-required admin routes (non-capsule + capsule list/create).
		r.Group(func(r chi.Router) {
			r.Use(requireSession(queries, sessionSvc))
			r.Use(requireAdmin(queries))
			r.Use(csrf)
			r.Get("/teams", teamH.AdminListTeams)
			r.Put("/teams/{id}/byoc", teamH.SetBYOC)
			r.Delete("/teams/{id}", teamH.AdminDeleteTeam)
			r.Get("/users", usersH.AdminListUsers)
			r.Put("/users/{id}/active", usersH.SetUserActive)
			r.Put("/users/{id}/admin", usersH.SetUserAdmin)
			r.Get("/audit-logs", auditH.AdminList)
			r.Get("/templates", buildH.ListTemplates)
			r.Delete("/templates/{name}", buildH.DeleteTemplate)
			r.Post("/builds", buildH.Create)
			r.Get("/builds", buildH.List)
			r.Get("/builds/{id}", buildH.Get)
			r.Post("/builds/{id}/cancel", buildH.Cancel)
			r.Post("/capsules", adminCapsules.Create)
			r.Get("/capsules", adminCapsules.List)
		})

		r.Route("/capsules/{id}", func(r chi.Router) {
			// Auth-required non-WS admin capsule routes.
			r.Group(func(r chi.Router) {
				r.Use(requireSession(queries, sessionSvc))
				r.Use(requireAdmin(queries))
				r.Use(csrf)
				r.Use(injectPlatformTeam())
				r.Get("/", adminCapsules.Get)
				r.Delete("/", adminCapsules.Destroy)
				r.Post("/snapshot", adminCapsules.Snapshot)
				r.Post("/exec", exec.Exec)
				r.Post("/files/write", files.Upload)
				r.Post("/files/read", files.Download)
				r.Post("/files/list", fsH.ListDir)
				r.Post("/files/mkdir", fsH.MakeDir)
				r.Post("/files/remove", fsH.Remove)
				r.Get("/metrics", metricsH.GetMetrics)
				r.Get("/processes", processH.ListProcesses)
				r.Delete("/processes/{selector}", processH.KillProcess)
			})

			// Admin WebSocket endpoints — browser auth via cookie + admin DB check
			// before upgrade. markAdminWS is retained as a context hint for any
			// admin-specific behavior downstream.
			r.Group(func(r chi.Router) {
				r.Use(requireSession(queries, sessionSvc))
				r.Use(requireAdmin(queries))
				r.Use(injectPlatformTeam())
				r.Get("/exec/stream", execStream.ExecStream)
				r.Get("/pty", ptyH.PtySession)
				r.Get("/processes/{selector}/stream", processH.ConnectProcess)
			})
		})
	})

	// Let extensions register their routes after all core routes.
	for _, ext := range extensions {
		ext.RegisterRoutes(r, sctx)
	}

	return &Server{router: r, BuildSvc: buildSvc, SSERelay: sseRelay, SessionSvc: sessionSvc, version: version}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.router
}

// Router returns the underlying chi.Router for direct access.
func (s *Server) Router() chi.Router {
	return s.router
}

func serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapiYAML)
}

func serveDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Wrenn API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.18.2/swagger-ui.css" integrity="sha384-rcbEi6xgdPk0iWkAQzT2F3FeBJXdG+ydrawGlfHAFIZG7wU6aKbQaRewysYpmrlW" crossorigin="anonymous">
  <style>
    body { margin: 0; background: #fafafa; }
    .swagger-ui .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.18.2/swagger-ui-bundle.js" integrity="sha384-NXtFPpN61oWCuN4D42K6Zd5Rt2+uxeIT36R7kpXBuY9tLnZorzrJ4ykpqwJfgjpZ" crossorigin="anonymous"></script>
  <script>
    SwaggerUIBundle({
      url: "/openapi.yaml",
      dom_id: "#swagger-ui",
      deepLinking: true,
    });
  </script>
</body>
</html>`)
}

// serviceEventToCanonical maps a SandboxService background-goroutine event
// into the canonical events.Event taxonomy for unified publishing. Returns
// false for events that should not be broadcast.
func serviceEventToCanonical(e service.SandboxStateEvent) (events.Event, bool) {
	var (
		eventType string
		outcome   events.Outcome
		metadata  map[string]string
	)
	switch e.Event {
	case "sandbox.started":
		eventType = events.CapsuleCreate
		outcome = events.OutcomeSuccess
	case "sandbox.resumed":
		eventType = events.CapsuleResume
		outcome = events.OutcomeSuccess
	case "sandbox.paused":
		eventType = events.CapsulePause
		outcome = events.OutcomeSuccess
	case "sandbox.auto_paused":
		eventType = events.CapsulePause
		outcome = events.OutcomeSuccess
		metadata = map[string]string{"reason": "ttl_expired"}
	case "sandbox.stopped":
		eventType = events.CapsuleDestroy
		outcome = events.OutcomeSuccess
	case "sandbox.pause_failed":
		eventType = events.CapsulePause
		outcome = events.OutcomeError
	case "sandbox.resume_failed":
		eventType = events.CapsuleResume
		outcome = events.OutcomeError
	default:
		return events.Event{}, false
	}
	if e.HostIP != "" {
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata["host_ip"] = e.HostIP
	}
	return events.Event{
		Event:     eventType,
		Outcome:   outcome,
		Timestamp: events.Now(),
		TeamID:    e.TeamID,
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: e.SandboxID, Type: "sandbox"},
		Metadata:  metadata,
		Error:     e.Error,
	}, true
}
