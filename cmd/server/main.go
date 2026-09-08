// Command devops-tools is a self-service portal for Kubernetes: developers get
// logs, metrics, restarts, secrets and Helm operations for the namespaces they
// are allowed to touch — without direct cluster access.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"devops-tools/internal/api"
	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/config"
	"devops-tools/internal/configsource"
	"devops-tools/internal/consulkv"
	"devops-tools/internal/gitlab"
	"devops-tools/internal/kube"
	"devops-tools/internal/loki"
	"devops-tools/internal/prom"
	"devops-tools/internal/rbac"
	"devops-tools/internal/store"
	"devops-tools/internal/version"
)

func main() {
	// Small helper so operators can produce a bcrypt hash for the local
	// provider without hunting for htpasswd:  devops-tools hash-password <pw>
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		hashPassword(os.Args[2:])
		return
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	loader, err := config.LoaderFromEnv()
	if err != nil {
		fail("init config loader", err)
	}
	slog.Info("loading configuration", "source", loader.Source())

	cfg, rbacCfg, err := config.Load(loader)
	if err != nil {
		fail("load configuration", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The store holds who has signed in and what they were granted. Opening it
	// before anything else means an unwritable volume stops the service here,
	// with a clear reason, rather than at the first sign-in.
	userStore, err := store.Open(cfg.Storage.Path)
	if err != nil {
		fail("open the access store", err)
	}
	slog.Info("access store ready", "path", cfg.Storage.Path, "users", userStore.Count())

	authSvc, err := auth.New(ctx, cfg.Auth, userStore)
	if err != nil {
		fail("init authentication", err)
	}
	defer authSvc.Close()
	slog.Info("authentication ready", "provider", authSvc.Kind())
	if len(cfg.Auth.Bootstrap.Users) > 0 {
		slog.Info("bootstrap administrators configured",
			"users", cfg.Auth.Bootstrap.Users, "roles", cfg.Auth.Bootstrap.Roles)
	}
	if cfg.Auth.Bootstrap.PasswordLogin.Enabled() {
		// A way in that the provider cannot revoke deserves to be visible in
		// the log of every start, not only in whoever's memory configured it.
		slog.Warn("break-glass password sign-in is enabled",
			"user", cfg.Auth.Bootstrap.PasswordLogin.Username, "path", "/signin/local")
	}

	if t := cfg.Auth.LDAP.TLS; cfg.Auth.Provider == config.ProviderLDAP {
		if t.InsecureAllowPlaintext {
			// Every sign-in sends somebody's password across the network in the
			// clear, and so does the bind that precedes it. There are networks
			// where that is accepted; there is no network where it should be
			// forgotten about.
			slog.Warn("ldap: the connection is not encrypted (auth.ldap.tls.insecureAllowPlaintext); " +
				"the bind password and every password typed into the portal cross the network in the clear")
		}
		if t.InsecureSkipVerify {
			slog.Warn("ldap: the directory's certificate is not verified " +
				"(auth.ldap.tls.insecureSkipVerify); the connection is encrypted against " +
				"eavesdropping but not against being answered by somebody else")
		}
	}

	if users := cfg.UsesShippedPassword(); len(users) > 0 {
		// The example password is published with the chart, so this is not a
		// weak password but a known one. Said at every start rather than once
		// in a comment somebody read while installing.
		slog.Warn("an account still has the example password shipped with the chart; "+
			"anyone who has read the repository can sign in as it",
			"users", users)
	}

	km, err := kube.NewManager(cfg.Cluster, cfg.Impersonation)
	if err != nil {
		fail("connect to kubernetes", err)
	}

	if len(rbacCfg.Declared) > 0 {
		// The list used to be written out in values, and keeping it in step with
		// the code was left to whoever remembered. Now it is derived; saying so
		// beats letting somebody edit a list that no longer does anything.
		slog.Warn("the configuration still declares `rbac.operations`; the list is now taken from "+
			"the build and this one is ignored — it can be deleted",
			"ignored", rbacCfg.Declared, "in_use", rbacCfg.Operations)
	}

	auditLog := audit.New()

	// Roles are edited in the portal and kept on the volume. The `rbac` section
	// of the configuration seeds an empty store and is then left alone —
	// reapplying it on
	// every start would undo, silently and without an error, whatever an
	// administrator had set up.
	seeded, err := userStore.SeedRoles(rbacCfg)
	if err != nil {
		fail("seed the access model", err)
	}
	if seeded {
		slog.Info("access model seeded from the configuration", "roles", len(rbacCfg.Roles))
	} else {
		slog.Info("access model loaded from the store; the `rbac` section of the configuration "+
			"is not applied after the first start",
			"roles", len(userStore.ListRoles()), "path", cfg.Storage.Path)
		// Editing rbacConfig.roles on a running installation does nothing, and
		// doing nothing quietly is how somebody spends an afternoon wondering
		// why their upgrade had no effect. Naming what differs turns a silent
		// no-op into a visible one.
		if diff := unappliedRoles(rbacCfg, userStore); len(diff) > 0 {
			slog.Warn("the `rbac` section names roles the store does not have, and it is not "+
				"applied after the first start — roles are edited in the portal, under Access "+
				"management, and live on the volume",
				"only_in_configuration", diff, "path", cfg.Storage.Path)
		}
	}
	authorizer := rbac.New(userStore.AccessModel(rbacCfg.Operations))

	var promClient *prom.Client
	if cfg.Metrics.PrometheusURL != "" {
		promClient = prom.New(cfg.Metrics.PrometheusURL)
		slog.Info("metrics enabled", "prometheus", cfg.Metrics.PrometheusURL)
	}

	var lokiClient *loki.Client
	if cfg.Logs.LokiURL != "" {
		lokiClient = loki.New(cfg.Logs.LokiURL, "")
		slog.Info("historical logs enabled", "loki", cfg.Logs.LokiURL)
	}

	// Tokens are deliberately not logged, nor whether there is one — a
	// deployment that forgot fails on the first request with the far more
	// specific error the other end returns.
	var consulSrc *configsource.Consul
	if cfg.Configs.Consul.Enabled {
		kv, err := consulkv.New(cfg.Configs.Consul.Address, cfg.Configs.Consul.Prefix)
		if err != nil {
			fail("connect to Consul for configuration management", err)
		}
		consulSrc = configsource.NewConsul(kv)
	}
	var gitSrc configsource.Source
	if g := cfg.Configs.Git; g.Enabled {
		// Listing walks the repository over the API, and every visit to the page
		// asks for it. Consul's listing is one call and needs no such help.
		c := configsource.Cache(
			configsource.NewGit(gitlab.New(g.URL, g.Token), g.Project, g.Branch, g.BasePath))
		// Fetched now, in the background, so the first person to open the page
		// is not the one who waits for it.
		c.Warm()
		gitSrc = c
	}

	var configs configsource.Source
	switch {
	case cfg.Configs.Paired():
		configs = configsource.NewPair(gitSrc, consulSrc)
		slog.Info("configuration management enabled", "source", "git+consul",
			"project", cfg.Configs.Git.Project, "branch", cfg.Configs.Git.Branch,
			"consul", cfg.Configs.Consul.Address, "prefix", cfg.Configs.Consul.Prefix)
	case gitSrc != nil:
		configs = gitSrc
		// The branch is worth its own mention: it is the one setting whose being
		// wrong is invisible until something is committed to the wrong environment.
		slog.Info("configuration management enabled", "source", "git",
			"gitlab", cfg.Configs.Git.URL, "project", cfg.Configs.Git.Project,
			"branch", cfg.Configs.Git.Branch, "basePath", cfg.Configs.Git.BasePath)
	case consulSrc != nil:
		c := configsource.Cache(consulSrc)
		c.Warm()
		configs = c
		slog.Info("configuration management enabled", "source", "consul",
			"consul", cfg.Configs.Consul.Address, "prefix", cfg.Configs.Consul.Prefix)
	}

	var rebuildClient *gitlab.Client
	if c := cfg.ImageRebuild; c.Enabled {
		rebuildClient = gitlab.New(c.URL, c.Token)
		slog.Info("rebuilding images enabled", "gitlab", c.URL,
			"environments", c.Environments, "buildJobs", c.BuildJobs, "deployJobs", c.DeployJobs)
	}

	srv := api.NewServer(cfg, authorizer, km, auditLog, authSvc, userStore,
		promClient, lokiClient, configs, rebuildClient).
		WithOperations(rbacCfg.Operations)

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("server starting", "addr", httpSrv.Addr, "environment", cfg.Environment,
			"version", version.Version)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fail("listen", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func fail(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}

// hashPassword prints a bcrypt hash for use in auth.local.users[].passwordHash.
func hashPassword(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: devops-tools hash-password <password>")
		os.Exit(2)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(args[0]), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(string(hash))
}

// unappliedRoles names roles present in the configuration but absent from
// the store — the ones somebody may be expecting an upgrade to create.
func unappliedRoles(cfg *config.RBACConfig, st *store.Store) []string {
	have := map[string]bool{}
	for _, r := range st.ListRoles() {
		have[r.Name] = true
	}
	var missing []string
	for name := range cfg.Roles {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
