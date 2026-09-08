package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/gitlab"
)

// Bringing back an image that a registry retention policy removed.
//
// The workload is still deployed and its tag still names the pipeline that
// produced it, so that build can simply be run again. Nothing else records the
// link between a running container and the pipeline behind it.
//
// Off unless the deployment turns it on: it depends on the image tag encoding a
// pipeline number and the image path matching the GitLab project, which is a
// house convention rather than anything universal.

// Ticket purposes. A ticket to watch a build is not a ticket to release a
// deploy, and keeping them apart is what stops the narrower being presented for
// the wider.
const (
	purposeWatchBuild = "rebuild:watch-build"
	purposeDeploy     = "rebuild:deploy"
)

// ticketTTL covers the work: a build takes minutes. A ticket that outlived it
// would be a standing permission to act on that project, granted once and
// remembered by nobody.
const ticketTTL = 2 * time.Hour

// requireImageRebuild answers 404 where the feature is off, so an installation
// without it has no endpoint rather than one that fails obscurely.
func (s *Server) requireImageRebuild(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.gitlab == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "rebuilding images is not enabled on this instance",
			})
			return
		}
		handler(w, r)
	}
}

// podPipeline works out which project and pipeline built the pod's image.
//
// Always from the pod, never from a parameter. What comes back is signed into a
// ticket and only that signature is trusted afterwards: a project name chosen
// by the caller would mean acting anywhere the GitLab token reaches, however
// narrow that person's actual access is.
func (s *Server) podPipeline(w http.ResponseWriter, r *http.Request) (gitlab.ImageRef, string, bool) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	image, err := s.kube.PodImage(r.Context(), user.Username, namespace, pod)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return gitlab.ImageRef{}, "", false
	}
	ref, err := gitlab.ParseImage(image, s.tagConvention())
	if err != nil {
		// Not a failure of the portal: this image was not built by a pipeline
		// this instance knows how to find. Saying which image it was is what
		// makes that checkable.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return gitlab.ImageRef{}, "", false
	}
	return ref, image, true
}

func (s *Server) tagConvention() gitlab.TagConvention {
	c := s.cfg.ImageRebuild
	return gitlab.TagConvention{
		Environments: c.Environments,
		BuildJobs:    c.BuildJobs,
		DeployJobs:   c.DeployJobs,
	}
}

// handleRebuildImage runs the build that produced this image again.
func (s *Server) handleRebuildImage(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	ref, image, ok := s.podPipeline(w, r)
	if !ok {
		return
	}

	res, err := s.gitlab.RetryBuildJob(r.Context(), ref.ProjectPath, ref.Pipeline, ref.BuildJobNames(s.tagConvention()))
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpImageRebuild, Target: image,
		Allowed: true, Success: err == nil, Error: errStr(err),
	})

	if errors.Is(err, gitlab.ErrPipelineGone) {
		// There is a way out — a fresh pipeline on the branch — but that also
		// deploys, so whether to offer it depends on a permission this caller
		// may not have. Deciding here beats a button that fails on being
		// pressed.
		branch := s.cfg.ImageRebuild.Branches[ref.Environment]
		writeJSON(w, http.StatusOK, map[string]any{
			"pipelineGone":     true,
			"branch":           branch,
			"canStartPipeline": s.authz.Allowed(user.Roles, namespace, OpPipelineDeploy),
			"message": fmt.Sprintf(
				"Pipeline #%d is gone, so its build cannot be run again. A fresh pipeline on %s would "+
					"rebuild and redeploy — which is a deployment, not just a rebuild.",
				ref.Pipeline, branch),
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	ticket, err := s.auth.IssueTicket(auth.Ticket{
		Purpose: purposeWatchBuild, Project: ref.ProjectPath,
		Pipeline: ref.Pipeline, Job: res.ID,
		Environment: ref.Environment, Namespace: namespace, Pod: pod,
	}, ticketTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	message := fmt.Sprintf("Job %s in pipeline #%d is running again; the image returns in a few minutes.",
		res.Name, ref.Pipeline)
	if res.AlreadyRunning {
		message = fmt.Sprintf("The build is already under way (job %s, pipeline #%d). Wait for it to finish.",
			res.Name, ref.Pipeline)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job": res.Name, "jobId": res.ID, "jobStatus": res.Status, "jobUrl": res.WebURL,
		"pipeline": ref.Pipeline, "alreadyRunning": res.AlreadyRunning,
		"ticket": ticket, "message": message,
	})
}

// handleBuildStatus follows the build job.
func (s *Server) handleBuildStatus(w http.ResponseWriter, r *http.Request) {
	t, ok := s.ticket(w, r, purposeWatchBuild)
	if !ok {
		return
	}
	status, webURL, err := s.gitlab.JobStatus(r.Context(), t.Project, t.Job)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobStatus": status, "jobUrl": webURL})
}

// handleStartPipeline starts a fresh pipeline on the branch.
//
// Gated on pipeline-deploy rather than image-rebuild. Where one pipeline both
// builds and deploys — which is the arrangement this exists for — starting one
// puts a new version into the environment. That it appears as the next button
// along from a rebuild does not make it the same act.
func (s *Server) handleStartPipeline(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	ref, image, ok := s.podPipeline(w, r)
	if !ok {
		return
	}
	branch := s.cfg.ImageRebuild.Branches[ref.Environment]
	if branch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("no branch is configured for environment %q", ref.Environment),
		})
		return
	}

	p, err := s.gitlab.CreatePipeline(r.Context(), ref.ProjectPath, branch)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpPipelineDeploy, Target: image + " (new pipeline on " + branch + ")",
		Allowed: true, Success: err == nil, Error: errStr(err),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	ticket, err := s.auth.IssueTicket(auth.Ticket{
		Purpose: purposeDeploy, Project: ref.ProjectPath, Pipeline: p.ID,
		Environment: ref.Environment, Namespace: namespace, Pod: pod,
	}, ticketTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pipelineId": p.ID, "pipelineUrl": p.WebURL, "status": p.Status,
		"branch": branch, "ticket": ticket,
	})
}

// handlePipelineStatus follows the fresh pipeline.
func (s *Server) handlePipelineStatus(w http.ResponseWriter, r *http.Request) {
	t, ok := s.ticket(w, r, purposeDeploy)
	if !ok {
		return
	}
	status, webURL, err := s.gitlab.PipelineStatus(r.Context(), t.Project, t.Pipeline)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	out := map[string]any{"status": status, "pipelineUrl": webURL}
	if status == "failed" {
		// Which job failed, so the answer is not a blind "run it again". A
		// build that failed its security scan will fail it again.
		out["failedJobs"] = nonNil(s.gitlab.FailedJobNames(r.Context(), t.Project, t.Pipeline))
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePlayDeploy releases the manual deploy job a pipeline is waiting on.
func (s *Server) handlePlayDeploy(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	t, ok := s.ticket(w, r, purposeDeploy)
	if !ok {
		return
	}
	// Which jobs may be released is the deployment's decision, by exact name,
	// for the environment the server itself worked out. Taken from a parameter
	// it would select another environment's job names — and release one of
	// those instead.
	allowed := gitlab.ImageRef{Environment: t.Environment}.DeployJobNames(s.tagConvention())

	played, manual, err := s.gitlab.PlayManualDeployJobs(r.Context(), t.Project, t.Pipeline, allowed)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: t.Namespace,
		Operation: OpPipelineDeploy,
		Target:    fmt.Sprintf("%s#%d released %v", t.Project, t.Pipeline, played),
		Allowed:   true, Success: err == nil, Error: errStr(err),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"played": nonNil(played),
		// Everything still waiting, so the page can say a person is needed
		// rather than leaving the pipeline looking stalled.
		"waiting": nonNil(manual),
	})
}

// ticket verifies the ticket accompanying a request.
func (s *Server) ticket(w http.ResponseWriter, r *http.Request, purpose string) (auth.Ticket, bool) {
	t, err := s.auth.VerifyTicket(r.URL.Query().Get("ticket"), purpose)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return auth.Ticket{}, false
	}
	return t, true
}
