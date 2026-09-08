// Package api exposes the HTTP API: routing, per-operation authorization and
// audit logging.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/config"
	"devops-tools/internal/configsource"
	"devops-tools/internal/gitlab"
	"devops-tools/internal/kube"
	"devops-tools/internal/loki"
	"devops-tools/internal/prom"
	"devops-tools/internal/rbac"
	"devops-tools/internal/store"
	"devops-tools/internal/ui"
)

// Operation names, aliased from the package that owns them.
//
// The list a role may draw on is derived from these — see config.Operations —
// rather than written out in values, so a name the code checks and a name a
// deployment may grant cannot drift apart.
const (
	OpLogs          = config.OpLogs
	OpDescribe      = config.OpDescribe
	OpMetrics       = config.OpMetrics
	OpPodRestart    = config.OpPodRestart
	OpHelmList      = config.OpHelmList
	OpHelmHistory   = config.OpHelmHistory
	OpHelmRollback  = config.OpHelmRollback
	OpHelmUninstall = config.OpHelmUninstall
	OpSecretList    = config.OpSecretList
	OpSecretRead    = config.OpSecretRead
	OpSecretCreate  = config.OpSecretCreate
	OpSecretUpdate  = config.OpSecretUpdate
	OpSecretDelete  = config.OpSecretDelete

	OpUserList   = config.OpUserList
	OpUserManage = config.OpUserManage

	OpConfigRead  = config.OpConfigRead
	OpConfigWrite = config.OpConfigWrite

	OpImageRebuild   = config.OpImageRebuild
	OpPipelineDeploy = config.OpPipelineDeploy
)

// Server holds the dependencies and builds the router.
type Server struct {
	cfg *config.Config
	// operations is the vocabulary the code understands, from the deployed
	// configuration. Roles are edited in the portal, but the operation names they
	// may use are not something an administrator invents.
	operations []string
	authz      *rbac.Authorizer
	kube       *kube.Manager
	audit      *audit.Recorder
	auth       *auth.Service
	store      *store.Store
	prom       *prom.Client        // nil when Prometheus is not configured
	loki       *loki.Client        // nil when Loki is not configured
	configs    configsource.Source // nil when configuration management is off
	gitlab     *gitlab.Client      // nil when rebuilding images is off
}

func NewServer(
	cfg *config.Config,
	authz *rbac.Authorizer,
	km *kube.Manager,
	al *audit.Recorder,
	as *auth.Service,
	st *store.Store,
	pc *prom.Client,
	lc *loki.Client,
	cs configsource.Source,
	gl *gitlab.Client,
) *Server {
	return &Server{
		cfg: cfg, authz: authz, kube: km, audit: al, auth: as, store: st,
		prom: pc, loki: lc, configs: cs, gitlab: gl,
	}
}

// Operations returns the operation names roles may grant.
func (s *Server) Operations() []string { return s.operations }

// WithOperations records the deployment's operation vocabulary.
func (s *Server) WithOperations(ops []string) *Server {
	s.operations = ops
	return s
}

// Router builds the chi router with every endpoint.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.cors)

	// Health and metrics are unauthenticated; restrict /metrics at the ingress
	// or network level if it should not be public.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", promhttp.Handler())

	// Unauthenticated: tells the browser which provider to use.
	r.Get("/api/auth/info", s.handleAuthInfo)
	r.Post("/api/login", s.handleLogin)

	r.Route("/api", func(r chi.Router) {
		r.Use(s.auth.Middleware)

		// An unknown /api path is a client bug, not a page — answer as an API.
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such endpoint"})
		})

		r.Get("/me", s.handleMe)
		r.Get("/namespaces", s.handleNamespaces)
		r.Get("/users", s.requireGlobalOp(OpUserList, s.handleUsers))
		r.Get("/roles", s.requireGlobalOp(OpUserList, s.handleListRoles))

		// Changing who can do what is a separate power from seeing it.
		r.Post("/users", s.requireGlobalOp(OpUserManage, s.handleCreateUser))
		// Roles prepared for somebody who has not signed in yet. Unlike creating
		// an account, this works under any provider — there is no password in it.
		r.Post("/users/invite", s.requireGlobalOp(OpUserManage, s.handleInvite))
		r.Put("/users/{username}/roles", s.requireGlobalOp(OpUserManage, s.handleSetRoles))
		r.Put("/users/{username}/password", s.requireGlobalOp(OpUserManage, s.handleSetPassword))
		r.Delete("/users/{username}", s.requireGlobalOp(OpUserManage, s.handleDeleteUser))
		r.Put("/roles/{name}", s.requireGlobalOp(OpUserManage, s.handleSaveRole))
		r.Delete("/roles/{name}", s.requireGlobalOp(OpUserManage, s.handleDeleteRole))
		// Who holds a role, changed for several people at once. Named after the
		// role because that is what changes, but served to the users screen —
		// see the handler.
		r.Post("/roles/{name}/members", s.requireGlobalOp(OpUserManage, s.handleRoleMembership))

		// Configuration. Not per namespace: the path is a query parameter
		// because it contains slashes, and the permission is checked against it
		// inside the handler rather than by requireOp.
		r.Get("/configurations", s.requireConfigs(s.handleListConfigs))
		// For the role editor, so that listing the tree is not paid for on
		// every load of Access management. Granting access is what needs it.
		r.Get("/configurations/paths",
			s.requireConfigs(s.requireGlobalOp(OpUserManage, s.handleConfigPaths)))
		r.Get("/configurations/entry", s.requireConfigs(s.handleGetConfig))
		r.Put("/configurations/entry", s.requireConfigs(s.handleSaveConfig))

		base := "/namespaces/{namespace}"

		// pods
		r.Get(base+"/pods", s.requireOp(OpLogs, s.handleListPods))
		r.Get(base+"/pods/{pod}/containers", s.requireOp(OpLogs, s.handlePodContainers))
		r.Get(base+"/pods/{pod}/logs", s.requireOp(OpLogs, s.handlePodLogs))
		r.Get(base+"/pods/{pod}/describe", s.requireOp(OpDescribe, s.handleDescribePod))
		r.Get(base+"/pods/{pod}/events", s.requireOp(OpDescribe, s.handlePodEvents))
		r.Get(base+"/pods/{pod}/metrics", s.requireOp(OpMetrics, s.handlePodMetrics))
		r.Post(base+"/pods/{pod}/restart", s.requireOp(OpPodRestart, s.handleRestartPod))

		// Rebuilding an image a registry retention policy removed. Everything
		// after the first call carries a ticket the server signed, so the
		// project and pipeline acted on are always ones it worked out itself.
		rebuild := base + "/pods/{pod}/rebuild-image"
		r.Post(rebuild, s.requireImageRebuild(s.requireOp(OpImageRebuild, s.handleRebuildImage)))
		r.Get(rebuild+"/status", s.requireImageRebuild(s.requireOp(OpImageRebuild, s.handleBuildStatus)))
		// These two put a version into the environment, so they are gated
		// separately — see the operation constants.
		r.Post(rebuild+"/pipeline", s.requireImageRebuild(s.requireOp(OpPipelineDeploy, s.handleStartPipeline)))
		r.Get(rebuild+"/pipeline/status", s.requireImageRebuild(s.requireOp(OpImageRebuild, s.handlePipelineStatus)))
		r.Post(rebuild+"/pipeline/deploy", s.requireImageRebuild(s.requireOp(OpPipelineDeploy, s.handlePlayDeploy)))
		r.Get(base+"/rollout-status", s.requireOp(OpLogs, s.handleRolloutStatus))
		r.Get(base+"/workload-pods", s.requireOp(OpLogs, s.handleWorkloadPods))

		// logs across pods
		r.Get(base+"/logs", s.requireOp(OpLogs, s.handleMultiPodLogs))
		r.Get(base+"/logs/query", s.requireOp(OpLogs, s.handleLokiQuery))

		// helm
		r.Get(base+"/helm/releases", s.requireOp(OpHelmList, s.handleListReleases))
		r.Get(base+"/helm/releases/{name}/history", s.requireOp(OpHelmHistory, s.handleReleaseHistory))
		r.Post(base+"/helm/releases/{name}/rollback", s.requireOp(OpHelmRollback, s.handleRollback))
		r.Delete(base+"/helm/releases/{name}", s.requireOp(OpHelmUninstall, s.handleUninstall))

		// secrets
		r.Get(base+"/secrets", s.requireOp(OpSecretList, s.handleListSecrets))
		r.Get(base+"/secrets/{name}", s.requireOp(OpSecretRead, s.handleGetSecret))
		r.Post(base+"/secrets", s.requireOp(OpSecretCreate, s.handleCreateSecret))
		r.Put(base+"/secrets/{name}", s.requireOp(OpSecretUpdate, s.handleUpdateSecret))
		r.Delete(base+"/secrets/{name}", s.requireOp(OpSecretDelete, s.handleDeleteSecret))
	})

	// Everything else is the single-page application, embedded in the binary.
	r.NotFound(ui.Handler().ServeHTTP)

	return r
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := s.cfg.Server.CORSOrigin; o != "" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireOp checks that the caller may perform op in the namespace taken from
// the URL, records denials in the audit log, and only then runs the handler.
func (s *Server) requireOp(op string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		namespace := chi.URLParam(r, "namespace")

		if !s.authz.Allowed(user.Roles, namespace, op) {
			s.audit.Log(audit.Entry{
				User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
				Operation: op, Allowed: false, Success: false,
			})
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		handler(w, r)
	}
}

// requireGlobalOp is requireOp for operations that belong to no namespace.
func (s *Server) requireGlobalOp(op string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if !s.authz.AllowedGlobal(user.Roles, op) {
			s.audit.Log(audit.Entry{
				User: user.Username, Environment: s.cfg.Environment, Namespace: "-",
				Operation: op, Allowed: false, Success: false,
			})
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		handler(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
