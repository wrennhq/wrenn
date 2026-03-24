package api

import (
	_ "embed"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/sandbox/internal/auth/oauth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	"git.omukk.dev/wrenn/sandbox/internal/scheduler"
	"git.omukk.dev/wrenn/sandbox/internal/service"
)

//go:embed openapi.yaml
var openapiYAML []byte

// Server is the control plane HTTP server.
type Server struct {
	router chi.Router
}

// New constructs the chi router and registers all routes.
func New(
	queries *db.Queries,
	pool *lifecycle.HostClientPool,
	sched scheduler.HostScheduler,
	pgPool *pgxpool.Pool,
	rdb *redis.Client,
	jwtSecret []byte,
	oauthRegistry *oauth.Registry,
	oauthRedirectURL string,
) *Server {
	r := chi.NewRouter()
	r.Use(requestLogger())

	// Shared service layer.
	sandboxSvc := &service.SandboxService{DB: queries, Pool: pool, Scheduler: sched}
	apiKeySvc := &service.APIKeyService{DB: queries}
	templateSvc := &service.TemplateService{DB: queries}
	hostSvc := &service.HostService{DB: queries, Redis: rdb, JWT: jwtSecret, Pool: pool}
	teamSvc := &service.TeamService{DB: queries, Pool: pgPool, HostPool: pool}

	sandbox := newSandboxHandler(sandboxSvc)
	exec := newExecHandler(queries, pool)
	execStream := newExecStreamHandler(queries, pool)
	files := newFilesHandler(queries, pool)
	filesStream := newFilesStreamHandler(queries, pool)
	snapshots := newSnapshotHandler(templateSvc, queries, pool)
	authH := newAuthHandler(queries, pgPool, jwtSecret)
	oauthH := newOAuthHandler(queries, pgPool, jwtSecret, oauthRegistry, oauthRedirectURL)
	apiKeys := newAPIKeyHandler(apiKeySvc)
	hostH := newHostHandler(hostSvc, queries)
	teamH := newTeamHandler(teamSvc)
	usersH := newUsersHandler(teamSvc)

	// OpenAPI spec and docs.
	r.Get("/openapi.yaml", serveOpenAPI)
	r.Get("/docs", serveDocs)

	// Unauthenticated auth endpoints.
	r.Post("/v1/auth/signup", authH.Signup)
	r.Post("/v1/auth/login", authH.Login)
	r.Get("/auth/oauth/{provider}", oauthH.Redirect)
	r.Get("/auth/oauth/{provider}/callback", oauthH.Callback)

	// JWT-authenticated: switch active team.
	r.With(requireJWT(jwtSecret)).Post("/v1/auth/switch-team", authH.SwitchTeam)

	// JWT-authenticated: API key management.
	r.Route("/v1/api-keys", func(r chi.Router) {
		r.Use(requireJWT(jwtSecret))
		r.Post("/", apiKeys.Create)
		r.Get("/", apiKeys.List)
		r.Delete("/{id}", apiKeys.Delete)
	})

	// JWT-authenticated: team management.
	r.Route("/v1/teams", func(r chi.Router) {
		r.Use(requireJWT(jwtSecret))
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
	r.With(requireJWT(jwtSecret)).Get("/v1/users/search", usersH.Search)

	// Sandbox lifecycle: accepts API key or JWT bearer token.
	r.Route("/v1/sandboxes", func(r chi.Router) {
		r.Use(requireAPIKeyOrJWT(queries, jwtSecret))
		r.Post("/", sandbox.Create)
		r.Get("/", sandbox.List)

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
			r.Use(requireJWT(jwtSecret))
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

	// Platform admin routes — require JWT + DB-validated admin status.
	r.Route("/v1/admin", func(r chi.Router) {
		r.Use(requireJWT(jwtSecret))
		r.Use(requireAdmin(queries))
		r.Put("/teams/{id}/byoc", teamH.SetBYOC)
	})

	return &Server{router: r}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
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
  <title>Wrenn Sandbox API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafafa; }
    .swagger-ui .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
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
