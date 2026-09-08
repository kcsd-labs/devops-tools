package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A retry creates a NEW job. Following the old id would poll something that
// never changes again, and the banner would sit at "running" forever while the
// build it is meant to be watching finishes without it.
func TestRetryFollowsTheNewJobNotTheOldOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			body, _ := json.Marshal([]job{
				{ID: 11, Name: "test-dev", Status: "success"},
				{ID: 12, Name: "docker-image-dev", Status: "failed"},
			})
			_, _ = w.Write(body)
			return
		}
		// The retry endpoint answers with the job it created.
		body, _ := json.Marshal(job{ID: 99, Name: "docker-image-dev", Status: "pending"})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	res, err := New(srv.URL, "t").RetryBuildJob(context.Background(), "g/p", 500,
		[]string{"docker-image-dev", "kaniko-image-dev"})
	if err != nil {
		t.Fatalf("RetryBuildJob: %v", err)
	}
	if res.ID != 99 {
		t.Errorf("ID = %d, want the id of the job the retry created (99)", res.ID)
	}
	if res.AlreadyRunning {
		t.Error("AlreadyRunning set on a job that was actually retried")
	}
}

// A job already underway must be left alone. GitLab answers 403 to a retry of a
// running job, which would be reported as a permissions problem and send
// somebody to check the token for no reason.
func TestRunningJobIsLeftAloneRatherThanRetried(t *testing.T) {
	retried := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			body, _ := json.Marshal([]job{{ID: 12, Name: "docker-image-dev", Status: "running"}})
			_, _ = w.Write(body)
			return
		}
		retried = true
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	res, err := New(srv.URL, "t").RetryBuildJob(context.Background(), "g/p", 500, []string{"docker-image-dev"})
	if err != nil {
		t.Fatalf("RetryBuildJob: %v", err)
	}
	if retried {
		t.Error("a running job was retried; GitLab would have answered 403")
	}
	if !res.AlreadyRunning || res.ID != 12 {
		t.Errorf("result = %+v, want the running job reported as already running", res)
	}
}

// Both cases lead to the same place — there is no build to run again — and the
// caller offers the same way out, a fresh pipeline on the branch.
func TestMissingPipelineAndMissingJobAreTheSamePredicament(t *testing.T) {
	t.Run("pipeline deleted", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		_, err := New(srv.URL, "t").RetryBuildJob(context.Background(), "g/p", 500, []string{"docker-image-dev"})
		if err != ErrPipelineGone {
			t.Fatalf("err = %v, want ErrPipelineGone", err)
		}
	})

	t.Run("build job cleaned out", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// The pipeline is there, but nothing in it builds an image.
			body, _ := json.Marshal([]job{{ID: 11, Name: "test-dev", Status: "success"}})
			_, _ = w.Write(body)
		}))
		defer srv.Close()
		_, err := New(srv.URL, "t").RetryBuildJob(context.Background(), "g/p", 500, []string{"docker-image-dev"})
		if err != ErrPipelineGone {
			t.Fatalf("err = %v, want ErrPipelineGone", err)
		}
	})
}

// Only the jobs the deployment named are started, matched exactly. Matching by
// shape — anything containing "deploy" — would release
// "deploy-to-production-now" from a portal meant to touch preprod, and the name
// alone is not permission.
func TestOnlyTheNamedJobsArePlayed(t *testing.T) {
	var played []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/play") {
			var id int
			_, _ = fmtSscan(r.URL.Path, &id)
			played = append(played, id)
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := json.Marshal([]job{
			{ID: 21, Name: "k8s-preprod-deploy-stage", Status: "manual"},
			{ID: 22, Name: "destroy-environment", Status: "manual"},
			// Contains "deploy", and is emphatically not what was asked for.
			{ID: 23, Name: "deploy-to-production", Status: "manual"},
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	playedNames, manual, err := New(srv.URL, "t").PlayManualDeployJobs(
		context.Background(), "g/p", 500, []string{"k8s-preprod-deploy-stage"})
	if err != nil {
		t.Fatalf("PlayManualDeployJobs: %v", err)
	}
	if len(playedNames) != 1 || playedNames[0] != "k8s-preprod-deploy-stage" {
		t.Errorf("played %v, want only the named job", playedNames)
	}
	if len(manual) != 2 {
		t.Errorf("manual = %v, want the other two left for a person", manual)
	}
	if len(played) != 1 || played[0] != 21 {
		t.Errorf("started job ids %v, want only 21", played)
	}
}

// Nothing named means nothing started. A deployment that has not said which
// jobs may be released has not authorised any.
func TestNoNamedJobsMeansNothingIsStarted(t *testing.T) {
	started := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/play") {
			started = true
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := json.Marshal([]job{{ID: 21, Name: "k8s-dev-deploy-stage", Status: "manual"}})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	played, manual, err := New(srv.URL, "t").PlayManualDeployJobs(context.Background(), "g/p", 500, nil)
	if err != nil {
		t.Fatalf("PlayManualDeployJobs: %v", err)
	}
	if started || len(played) != 0 {
		t.Error("a job was started although the deployment named none")
	}
	if len(manual) != 1 {
		t.Errorf("manual = %v, want the waiting job reported", manual)
	}
}

// A job that could not be started is reported as still waiting rather than as
// started. Saying it ran when it did not is worse than saying nothing.
func TestADeployJobThatWillNotStartIsReportedAsStillManual(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/play") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body, _ := json.Marshal([]job{{ID: 21, Name: "deploy-preprod", Status: "manual"}})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	played, manual, err := New(srv.URL, "t").PlayManualDeployJobs(
		context.Background(), "g/p", 500, []string{"deploy-preprod"})
	if err != nil {
		t.Fatalf("PlayManualDeployJobs: %v", err)
	}
	if len(played) != 0 {
		t.Errorf("played = %v, want nothing — the job was refused", played)
	}
	if len(manual) != 1 || manual[0] != "deploy-preprod" {
		t.Errorf("manual = %v, want the refused job still listed as waiting", manual)
	}
}

// So a failed pipeline can be explained rather than met with "try again": a
// build that failed its scan will fail it again.
func TestFailedJobsAreNamed(t *testing.T) {
	var scope string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope = r.URL.Query().Get("scope[]")
		body, _ := json.Marshal([]job{{ID: 31, Name: "container-scanning", Status: "failed"}})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	names := New(srv.URL, "t").FailedJobNames(context.Background(), "g/p", 500)
	if scope != "failed" {
		t.Errorf("asked GitLab for scope %q, want %q", scope, "failed")
	}
	if len(names) != 1 || names[0] != "container-scanning" {
		t.Errorf("names = %v, want the failed job named", names)
	}
}

// The new pipeline's own number is returned. Following "the latest on the
// branch" instead would sooner or later follow somebody else's push.
func TestCreatePipelineReturnsItsOwnNumber(t *testing.T) {
	var sentRef string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		sentRef = body["ref"]
		_, _ = w.Write([]byte(`{"id":7001,"status":"created","web_url":"https://gitlab/x/-/pipelines/7001"}`))
	}))
	defer srv.Close()

	p, err := New(srv.URL, "t").CreatePipeline(context.Background(), "g/p", "dev")
	if err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	if sentRef != "dev" {
		t.Errorf("ref = %q, want dev", sentRef)
	}
	if p.ID != 7001 || p.WebURL == "" {
		t.Errorf("pipeline = %+v, want its number and link", p)
	}
}

// fmtSscan pulls the job id out of ".../jobs/21/play".
func fmtSscan(path string, id *int) (int, error) {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "jobs" && i+1 < len(parts) {
			var n int
			for _, r := range parts[i+1] {
				if r < '0' || r > '9' {
					return 0, nil
				}
				n = n*10 + int(r-'0')
			}
			*id = n
			return 1, nil
		}
	}
	return 0, nil
}
