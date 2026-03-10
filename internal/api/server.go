package api

import (
	_ "embed"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

//go:embed openapi.yaml
var openapiYAML []byte

// Server is the control plane HTTP server.
type Server struct {
	router chi.Router
}

// New constructs the chi router and registers all routes.
func New(queries *db.Queries, agent hostagentv1connect.HostAgentServiceClient) *Server {
	r := chi.NewRouter()
	r.Use(requestLogger())

	sandbox := newSandboxHandler(queries, agent)
	exec := newExecHandler(queries, agent)
	execStream := newExecStreamHandler(queries, agent)
	files := newFilesHandler(queries, agent)
	filesStream := newFilesStreamHandler(queries, agent)

	// OpenAPI spec and docs.
	r.Get("/openapi.yaml", serveOpenAPI)
	r.Get("/docs", serveDocs)

	// Sandbox CRUD.
	r.Route("/v1/sandboxes", func(r chi.Router) {
		r.Post("/", sandbox.Create)
		r.Get("/", sandbox.List)

		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", sandbox.Get)
			r.Delete("/", sandbox.Destroy)
			r.Post("/exec", exec.Exec)
			r.Get("/exec/stream", execStream.ExecStream)
			r.Post("/pause", sandbox.Pause)
			r.Post("/resume", sandbox.Resume)
			r.Post("/files/write", files.Upload)
			r.Post("/files/read", files.Download)
			r.Post("/files/stream/write", filesStream.StreamUpload)
			r.Post("/files/stream/read", filesStream.StreamDownload)
		})
	})

	return &Server{router: r}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.router
}

func serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Write(openapiYAML)
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
