package api

import (
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
	"git.omukk.dev/wrenn/wrenn/pkg/channels"
	"git.omukk.dev/wrenn/wrenn/pkg/cpextension"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/scheduler"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

//go:embed openapi.yaml
var openapiYAML []byte

// Server is the control plane HTTP server.
type Server struct {
	router   chi.Router
	BuildSvc *service.BuildService
}

// New constructs the chi router and registers all routes.
// Extensions are called after core routes are registered, allowing enterprise
// or third-party code to add routes and middleware.
func New(
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
	channelSvc *channels.Service,
	mailer email.Mailer,
	extensions []cpextension.Extension,
	sctx cpextension.ServerContext,
) *Server {
	r := chi.NewRouter()
	r.Use(requestLogger())

	// Shared service layer.
	sandboxSvc := &service.SandboxService{DB: queries, Pool: pool, Scheduler: sched}
	apiKeySvc := &service.APIKeyService{DB: queries}
	templateSvc := &service.TemplateService{DB: queries}
	hostSvc := &service.HostService{DB: queries, Redis: rdb, JWT: jwtSecret, Pool: pool, CA: ca}
	teamSvc := &service.TeamService{DB: queries, Pool: pgPool, HostPool: pool}
	userSvc := &service.UserService{DB: queries}
	auditSvc := &service.AuditService{DB: queries}
	statsSvc := &service.StatsService{DB: queries, Pool: pgPool}
	buildSvc := &service.BuildService{DB: queries, Redis: rdb, Pool: pool, Scheduler: sched}

	sandbox := newSandboxHandler(sandboxSvc, al)
	exec := newExecHandler(queries, pool)
	execStream := newExecStreamHandler(queries, pool)
	files := newFilesHandler(queries, pool)
	filesStream := newFilesStreamHandler(queries, pool)
	fsH := newFSHandler(queries, pool)
	snapshots := newSnapshotHandler(templateSvc, queries, pool, al)
	authH := newAuthHandler(queries, pgPool, jwtSecret, mailer)
	oauthH := newOAuthHandler(queries, pgPool, jwtSecret, oauthRegistry, oauthRedirectURL)
	apiKeys := newAPIKeyHandler(apiKeySvc, al)
	hostH := newHostHandler(hostSvc, queries, al)
	teamH := newTeamHandler(teamSvc, al, mailer)
	usersH := newUsersHandler(queries, userSvc)
	auditH := newAuditHandler(auditSvc)
	statsH := newStatsHandler(statsSvc)
	metricsH := newSandboxMetricsHandler(queries, pool)
	buildH := newBuildHandler(buildSvc, queries, pool)
	channelH := newChannelHandler(channelSvc, al)
	ptyH := newPtyHandler(queries, pool)
	processH := newProcessHandler(queries, pool)
	adminCapsules := newAdminCapsuleHandler(sandboxSvc, queries, pool, al)
	meH := newMeHandler(queries, pgPool, rdb, jwtSecret, mailer, oauthRegistry, oauthRedirectURL)

	// OpenAPI spec and docs.
	r.Get("/openapi.yaml", serveOpenAPI)
	r.Get("/docs", serveDocs)

	// Unauthenticated auth endpoints.
	r.Post("/v1/auth/signup", authH.Signup)
	r.Post("/v1/auth/login", authH.Login)
	r.Get("/auth/oauth/{provider}", oauthH.Redirect)
	r.Get("/auth/oauth/{provider}/callback", oauthH.Callback)

	// Unauthenticated: password reset request and confirmation.
	r.Post("/v1/me/password/reset", meH.RequestPasswordReset)
	r.Post("/v1/me/password/reset/confirm", meH.ConfirmPasswordReset)

	// JWT-authenticated: self-service account management.
	r.Route("/v1/me", func(r chi.Router) {
		r.Use(requireJWT(jwtSecret, queries))
		r.Get("/", meH.GetMe)
		r.Patch("/", meH.UpdateName)
		r.Post("/password", meH.ChangePassword)
		r.Get("/providers/{provider}/connect", meH.ConnectProvider)
		r.Delete("/providers/{provider}", meH.DisconnectProvider)
		r.Delete("/", meH.DeleteAccount)
	})

	// JWT-authenticated: switch active team.
	r.With(requireJWT(jwtSecret, queries)).Post("/v1/auth/switch-team", authH.SwitchTeam)

	// JWT-authenticated: API key management.
	r.Route("/v1/api-keys", func(r chi.Router) {
		r.Use(requireJWT(jwtSecret, queries))
		r.Post("/", apiKeys.Create)
		r.Get("/", apiKeys.List)
		r.Delete("/{id}", apiKeys.Delete)
	})

	// JWT-authenticated: team management.
	r.Route("/v1/teams", func(r chi.Router) {
		r.Use(requireJWT(jwtSecret, queries))
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

	// JWT-authenticated: user search (for add-member UI).
	r.With(requireJWT(jwtSecret, queries)).Get("/v1/users/search", usersH.Search)

	// Capsule lifecycle: accepts API key or JWT bearer token.
	r.Route("/v1/capsules", func(r chi.Router) {
		r.Use(requireAPIKeyOrJWT(queries, jwtSecret))
		r.Post("/", sandbox.Create)
		r.Get("/", sandbox.List)
		r.Get("/stats", statsH.GetStats)

		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", sandbox.Get)
			r.Delete("/", sandbox.Destroy)
			r.Post("/exec", exec.Exec)
			r.Get("/exec/stream", execStream.ExecStream)
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
			r.Get("/pty", ptyH.PtySession)
			r.Get("/processes", processH.ListProcesses)
			r.Delete("/processes/{selector}", processH.KillProcess)
			r.Get("/processes/{selector}/stream", processH.ConnectProcess)
		})
	})

	// Snapshot / template management: accepts API key or JWT bearer token.
	r.Route("/v1/snapshots", func(r chi.Router) {
		r.Use(requireAPIKeyOrJWT(queries, jwtSecret))
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

		// Host-token-authenticated: heartbeat.
		r.With(requireHostToken(jwtSecret)).Post("/{id}/heartbeat", hostH.Heartbeat)

		// JWT-authenticated: host CRUD and tags.
		r.Group(func(r chi.Router) {
			r.Use(requireJWT(jwtSecret, queries))
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

	// JWT-authenticated: notification channels.
	r.Route("/v1/channels", func(r chi.Router) {
		r.Use(requireJWT(jwtSecret, queries))
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

	// JWT-authenticated: audit log.
	r.With(requireJWT(jwtSecret, queries)).Get("/v1/audit-logs", auditH.List)

	// Platform admin routes — require JWT + DB-validated admin status.
	r.Route("/v1/admin", func(r chi.Router) {
		r.Use(requireJWT(jwtSecret, queries))
		r.Use(requireAdmin(queries))
		r.Get("/teams", teamH.AdminListTeams)
		r.Put("/teams/{id}/byoc", teamH.SetBYOC)
		r.Delete("/teams/{id}", teamH.AdminDeleteTeam)
		r.Get("/users", usersH.AdminListUsers)
		r.Put("/users/{id}/active", usersH.SetUserActive)
		r.Get("/templates", buildH.ListTemplates)
		r.Delete("/templates/{name}", buildH.DeleteTemplate)
		r.Post("/builds", buildH.Create)
		r.Get("/builds", buildH.List)
		r.Get("/builds/{id}", buildH.Get)
		r.Post("/builds/{id}/cancel", buildH.Cancel)
		r.Post("/capsules", adminCapsules.Create)
		r.Get("/capsules", adminCapsules.List)
		r.Route("/capsules/{id}", func(r chi.Router) {
			r.Use(injectPlatformTeam())
			r.Get("/", adminCapsules.Get)
			r.Delete("/", adminCapsules.Destroy)
			r.Post("/snapshot", adminCapsules.Snapshot)
			r.Post("/exec", exec.Exec)
			r.Get("/exec/stream", execStream.ExecStream)
			r.Post("/files/write", files.Upload)
			r.Post("/files/read", files.Download)
			r.Post("/files/list", fsH.ListDir)
			r.Post("/files/mkdir", fsH.MakeDir)
			r.Post("/files/remove", fsH.Remove)
			r.Get("/metrics", metricsH.GetMetrics)
			r.Get("/pty", ptyH.PtySession)
			r.Get("/processes", processH.ListProcesses)
			r.Delete("/processes/{selector}", processH.KillProcess)
			r.Get("/processes/{selector}/stream", processH.ConnectProcess)
		})
	})

	// Let extensions register their routes after all core routes.
	for _, ext := range extensions {
		ext.RegisterRoutes(r, sctx)
	}

	return &Server{router: r, BuildSvc: buildSvc}
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
