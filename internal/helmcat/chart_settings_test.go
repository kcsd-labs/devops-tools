package helmcat

import (
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
)

func subchart(name, desc, values string) *chart.Chart {
	return &chart.Chart{
		Metadata: &chart.Metadata{Name: name, Description: desc},
		Raw:      []*chart.File{{Name: "values.yaml", Data: []byte(values)}},
	}
}

// A chart is read with the parts it is built from: a subchart's volumes are
// asked about under its name, a part switched off is not.
func TestDetectChart(t *testing.T) {
	parentValues := `
persistence:
  enabled: true
  size: 10Gi
  storageClassName: ""
logs:
  persistence:
    enabled: false
    size: 100Gi
flower:
  enabled: false
  replicas: 1
postgresql:
  enabled: true
  auth: {password: postgres}
cache:
  enabled: false
`
	pg := subchart("postgresql", "PostgreSQL is an object-relational database.", `
global:
  storageClass: ""
## @param primary.persistence.size PVC Storage Request for PostgreSQL volume
primary:
  persistence:
    size: 8Gi
    storageClass: ""
`)
	redis := subchart("redis", "Redis is a key-value store.", `
master:
  persistence:
    size: 8Gi
`)
	p := &chart.Chart{
		Metadata: &chart.Metadata{Name: "app", Dependencies: []*chart.Dependency{
			{Name: "postgresql", Condition: "postgresql.enabled"},
			{Name: "redis", Alias: "cache", Condition: "cache.enabled"},
		}},
		Raw: []*chart.File{{Name: "values.yaml", Data: []byte(parentValues)}},
	}
	p.Values = values(t, parentValues)
	pg.Values = values(t, string(pg.Raw[0].Data))
	redis.Values = values(t, string(redis.Raw[0].Data))
	p.AddDependency(pg, redis)

	s := DetectChart(p)
	if strings.Join(s.Disks, ",") != "persistence.size,postgresql.primary.persistence.size" {
		t.Errorf("disks %v: logs and the cache are off", s.Disks)
	}
	if strings.Join(s.Storage, ",") != "global.storageClass,persistence.storageClassName,postgresql.primary.persistence.storageClass" {
		t.Errorf("storage %v: a subchart's global does not cover the chart's own", s.Storage)
	}
	if s.Parts["postgresql"] == "" || len(s.Parts) != 1 {
		t.Errorf("parts %v", s.Parts)
	}
	keys := map[string]Component{}
	for _, c := range s.Components {
		keys[c.Key] = c
	}
	if c := keys["postgresql.enabled"]; !c.On || !c.Part {
		t.Errorf("postgresql switch %+v", c)
	}
	if c, ok := keys["cache.enabled"]; !ok || c.On || !c.Part {
		t.Errorf("an aliased subchart switched off: %+v", c)
	}
	if c, ok := keys["flower.enabled"]; !ok || c.On || c.Part {
		t.Errorf("a part of the chart's own: %+v", c)
	}
	if _, ok := keys["persistence.enabled"]; ok {
		t.Error("persistence configures, it is not a part")
	}
	if d := ChartDocs(p)["postgresql.primary.persistence.size"]; d != "PVC Storage Request for PostgreSQL volume" {
		t.Errorf("a subchart's docs under its name: %q", d)
	}
}

func TestDiskSizesEach(t *testing.T) {
	out, _, err := ApplyFill("", Fill{DiskSizes: map[string]string{"postgresql.primary.persistence.size": "20Gi", "persistence.size": ""}}, Fill{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "size: 20Gi") || strings.Contains(out, "persistence:\n  size") {
		t.Errorf("each volume its own size:\n%s", out)
	}
	if _, _, err := ApplyFill("", Fill{DiskSizes: map[string]string{"a.size": "lots"}}, Fill{}); err == nil {
		t.Error("a size that is not one")
	}
}

func TestPasswordsAndGit(t *testing.T) {
	s := Detect(values(t, `
createUserJob:
  defaultUser:
    username: admin
    password: admin
data:
  metadataConnection:
    user: postgres
    pass: postgres
postgresql:
  auth:
    postgresPassword: postgres
    username: ""
    password: ""
redis:
  password: ~
dags:
  gitSync:
    enabled: false
    repo: https://github.com/apache/airflow.git
    branch: v2-2-stable
    ref: v2-2-stable
    subPath: "tests/dags"
`))
	got := map[string][]string{}
	for _, p := range s.Passwords {
		got[p.Default] = p.Paths
	}
	if strings.Join(got["postgres"], ",") != "data.metadataConnection.pass,postgresql.auth.postgresPassword" {
		t.Errorf("the database's password, told twice, goes together: %+v", s.Passwords)
	}
	if strings.Join(got["admin"], ",") != "createUserJob.defaultUser.password" || strings.Join(got[""], ",") != "redis.password" {
		t.Errorf("passwords %+v (postgresql.auth.password has no user and is not asked)", s.Passwords)
	}
	if len(s.GitSources) != 1 || s.GitSources[0].Path != "dags.gitSync" || !s.GitSources[0].GitSync ||
		strings.Join(s.GitSources[0].BranchKeys, ",") != "branch,ref" || s.GitSources[0].FolderKey != "subPath" {
		t.Fatalf("git %+v", s.GitSources)
	}
	g := s.GitSources[0]
	g.CredentialsKey = "credentialsSecret"
	out, _, err := ApplyFill("", Fill{Git: &GitFill{GitSpot: g, Repo: "https://git.example.com/team/dags.git", Branch: "main",
		Credentials: "app-git"}, Passwords: map[string]string{"redis.password": "s3cret"}}, Fill{})
	if err != nil {
		t.Fatal(err)
	}
	gs := values(t, out)["dags"].(map[string]any)["gitSync"].(map[string]any)
	if gs["enabled"] != true || gs["ref"] != "main" || gs["branch"] != "main" || gs["subPath"] != "" || gs["credentialsSecret"] != "app-git" {
		t.Errorf("git:\n%s", out)
	}
	if !strings.Contains(out, "password: s3cret # password, from the install form") {
		t.Errorf("password:\n%s", out)
	}
}

func TestClusterDomain(t *testing.T) {
	s := Detect(values(t, `
clusterDomain: cluster.local
global:
  clusterDomain: cluster.local
replica:
  replicaCount: 3
`))
	if strings.Join(s.ClusterDomain, ",") != "clusterDomain,global.clusterDomain" || s.Defaults["clusterDomain"] != "cluster.local" {
		t.Fatalf("cluster domain %v", s.ClusterDomain)
	}
	out, _, err := ApplyFill("clusterDomain: cluster.local\n", Fill{ClusterDomain: "k8s.example", ClusterDomainPaths: s.ClusterDomain}, Fill{})
	if err != nil || !strings.Contains(out, "clusterDomain: k8s.example # the cluster's domain") {
		t.Errorf("fill %v:\n%s", err, out)
	}
	if _, _, err := ApplyFill("", Fill{ClusterDomain: "a b", ClusterDomainPaths: []string{"clusterDomain"}}, Fill{}); err == nil {
		t.Error("not a domain")
	}
}
