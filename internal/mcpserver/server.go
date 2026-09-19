// Package mcpserver exposes Envoryx to AI assistants through the Model Context Protocol.
//
// Every tool goes through the same project manager as the REST API and the UI, so the
// same validation, label guards, locks and audit logging apply. Destructive operations
// (deleting projects, dropping databases, restoring backups) are intentionally not
// exposed as tools.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Links describes how projects are reachable so tools can return URLs.
type Links struct {
	// PublicHost is the host for direct port links ("" = unknown).
	PublicHost func(ctx context.Context) string
	// HTTPPort / HTTPSPort are the host-side proxy ports (0 = not published).
	HTTPPort, HTTPSPort int
}

// Deps are the collaborators of the MCP server.
type Deps struct {
	Projects *project.Manager
	Catalog  *runtime.Catalog
	Auth     *auth.Service
	Links    Links
	Version  string
	Log      *slog.Logger
}

// Server is the MCP server plus its HTTP transport.
type Server struct {
	d   Deps
	mcp *mcp.Server
	// scopes is the token scope each tool needs, filled during registration.
	scopes map[string]auth.Scope
}

// New builds the server and registers all tools.
func New(d Deps) *Server {
	s := &Server{d: d, scopes: map[string]auth.Scope{}}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "envoryx", Title: "Envoryx", Version: d.Version, WebsiteURL: "https://github.com/envoryx/envoryx"}, &mcp.ServerOptions{
		Instructions: "Envoryx manages Docker-based development environments (PHP, web server, database, Redis, Mailpit, Node). " +
			"Projects are identified by id, slug or name. Use list_runtimes to see available versions before creating projects. " +
			"Deleting projects, dropping databases and restoring backups are not available here; ask the user to do that in the Envoryx UI. " +
			"Tools may be refused because of the token's scope (read < operate < admin) or its project restriction; the refusal names what is needed – ask the user for a token with that scope rather than retrying.",
		Logger: d.Log,
	})
	s.registerTools()
	s.mcp.AddReceivingMiddleware(s.enforceScope)
	return s
}

// enforceScope refuses tool calls the token's scope does not cover. The refusal is a tool
// result, not a protocol error, so the model learns what the token may do. Listing tools
// stays unrestricted: the client sees the full catalogue and gets told on use.
func (s *Server) enforceScope(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if call, ok := req.(*mcp.CallToolRequest); ok && method == "tools/call" {
			p, _ := auth.PrincipalFrom(ctx)
			need, known := s.scopes[call.Params.Name]
			if !known {
				need = auth.ScopeAdmin
			}
			if err := p.Require(need, ""); err != nil {
				return toolErr(err)
			}
		}
		return next(ctx, method, req)
	}
}

// tool records the scope a tool needs and returns it for registration.
func (s *Server) tool(need auth.Scope, t *mcp.Tool) *mcp.Tool {
	s.scopes[t.Name] = need
	return t
}

// Handler returns the HTTP handler: bearer-token authentication in front of the
// streamable HTTP transport. Session cookies are deliberately not accepted so that a
// browser can never be tricked into calling tools.
func (s *Server) Handler() http.Handler {
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       s.d.Log,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := auth.BearerToken(r)
		if bearer == "" {
			unauthorized(w, "missing bearer token")
			return
		}
		p, err := s.d.Auth.ValidateAPIToken(r.Context(), bearer)
		if err != nil {
			if !errors.Is(err, auth.ErrUnauthenticated) {
				s.d.Log.Error("api token validation failed", "err", err)
			}
			unauthorized(w, "invalid token")
			return
		}
		transport.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="envoryx"`)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `{"error":{"code":"unauthorized","message":%q}}`, msg)
}

// MCP exposes the underlying server (used by in-memory tests).
func (s *Server) MCP() *mcp.Server { return s.mcp }

// resolve finds a project by id, slug or name. A token confined to particular projects
// sees the others as non-existent.
func (s *Server) resolve(ctx context.Context, ref string) (project.View, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return project.View{}, fmt.Errorf("%w: project is required", validate.ErrInvalid)
	}
	p, _ := auth.PrincipalFrom(ctx)
	notFound := fmt.Errorf("%w: no project matches %q", store.ErrNotFound, ref)
	if validate.UUID(ref) == nil {
		v, err := s.d.Projects.Get(ctx, ref)
		if err == nil && !p.CanAccessProject(v.Project.ID) {
			return project.View{}, notFound
		}
		return v, err
	}
	views, err := s.visibleProjects(ctx)
	if err != nil {
		return project.View{}, err
	}
	slug := validate.Slugify(ref)
	for _, v := range views {
		if v.Project.Slug == ref || v.Project.Slug == slug || strings.EqualFold(v.Project.Name, ref) {
			return v, nil
		}
	}
	return project.View{}, notFound
}

// visibleProjects lists the projects the calling token may see.
func (s *Server) visibleProjects(ctx context.Context) ([]project.View, error) {
	views, err := s.d.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	p, _ := auth.PrincipalFrom(ctx)
	if !p.Restricted() {
		return views, nil
	}
	out := views[:0]
	for _, v := range views {
		if p.CanAccessProject(v.Project.ID) {
			out = append(out, v)
		}
	}
	return out, nil
}
