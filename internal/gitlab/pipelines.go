package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Running a build again, and following what happens next.

// ErrPipelineGone means the pipeline that produced the image is no longer there
// — deleted, or cleaned out along with its jobs. Retrying is then impossible,
// and the only way back is a fresh pipeline on the branch.
var ErrPipelineGone = errors.New("the pipeline is gone, so its build cannot be run again")

type job struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

// JobResult is what happened to a build job.
type JobResult struct {
	Name   string `json:"name"`
	ID     int    `json:"id"`
	Status string `json:"status"`
	WebURL string `json:"webUrl"`
	// AlreadyRunning means it was underway and was left alone. Retrying a
	// running job is refused by GitLab with a 403, which would be reported as a
	// permissions problem and send people looking for the wrong thing.
	AlreadyRunning bool `json:"alreadyRunning"`
}

// InProgress reports whether a status means the job has not finished.
func InProgress(status string) bool {
	switch status {
	case "created", "pending", "running", "preparing", "waiting_for_resource", "scheduled":
		return true
	}
	return false
}

// RetryBuildJob finds the build job in a pipeline and runs it again.
func (c *Client) RetryBuildJob(ctx context.Context, projectPath string, pipeline int, jobNames []string) (JobResult, error) {
	proj := url.PathEscape(projectPath)

	var jobs []job
	u := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs?per_page=100", c.base, proj, pipeline)
	if err := c.getJSON(ctx, u, &jobs); err != nil {
		if errors.Is(err, ErrNotFound) {
			return JobResult{}, ErrPipelineGone
		}
		return JobResult{}, err
	}

	want := make(map[string]bool, len(jobNames))
	for _, n := range jobNames {
		want[n] = true
	}
	var target *job
	for i := range jobs {
		if want[jobs[i].Name] {
			target = &jobs[i]
			break
		}
	}
	if target == nil {
		// The pipeline exists but its build job does not — cleaned out, or
		// renamed since. Same predicament as a deleted pipeline, so it is
		// reported the same way and offered the same way out.
		return JobResult{}, ErrPipelineGone
	}

	if InProgress(target.Status) {
		return JobResult{
			Name: target.Name, ID: target.ID, Status: target.Status,
			WebURL: target.WebURL, AlreadyRunning: true,
		}, nil
	}

	retry := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/retry", c.base, proj, target.ID)
	resp, err := c.post(ctx, retry, nil)
	if err != nil {
		return JobResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return JobResult{}, errors.New(
			"GitLab refused to run the job again (403) — either the token may not, or the pipeline is archived")
	}
	if resp.StatusCode/100 != 2 {
		return JobResult{}, fmt.Errorf("retrying the job returned %d", resp.StatusCode)
	}
	// A retry creates a new job; its id is the one to follow, not the old one.
	var fresh job
	_ = json.NewDecoder(resp.Body).Decode(&fresh)
	if fresh.ID == 0 {
		fresh = *target
	}
	return JobResult{Name: fresh.Name, ID: fresh.ID, Status: fresh.Status, WebURL: fresh.WebURL}, nil
}

// PipelineResult identifies a pipeline that was just started.
type PipelineResult struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
	WebURL string `json:"webUrl"`
}

// CreatePipeline starts a fresh pipeline on a branch.
//
// The way out when the original pipeline is gone. The new pipeline's number is
// returned and followed directly: picking "the latest on the branch" afterwards
// would sooner or later follow somebody else's push.
func (c *Client) CreatePipeline(ctx context.Context, projectPath, ref string) (PipelineResult, error) {
	proj := url.PathEscape(projectPath)
	u := fmt.Sprintf("%s/api/v4/projects/%s/pipeline", c.base, proj)
	body, _ := json.Marshal(map[string]string{"ref": ref})

	resp, err := c.post(ctx, u, body)
	if err != nil {
		return PipelineResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		return PipelineResult{}, fmt.Errorf(
			"could not start a pipeline on %q — no such branch, or no .gitlab-ci.yml on it", ref)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return PipelineResult{}, fmt.Errorf("starting a pipeline returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var raw struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
		WebURL string `json:"web_url"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	return PipelineResult{ID: raw.ID, Status: raw.Status, WebURL: raw.WebURL}, nil
}

// FailedJobNames names the jobs that failed.
//
// So that a failed pipeline can be explained rather than met with "try again":
// a build that failed its security scan will fail it again, and knowing which
// job it was is the difference between fixing something and repeating it.
func (c *Client) FailedJobNames(ctx context.Context, projectPath string, pipeline int) []string {
	jobs, err := c.pipelineJobs(ctx, projectPath, pipeline, "failed")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(jobs))
	for _, j := range jobs {
		names = append(names, j.Name)
	}
	return names
}

// PlayManualDeployJobs releases the named manual jobs of a pipeline.
//
// Some environments hold the final deploy behind a manual gate, and a pipeline
// left at that gate looks stalled while it is in fact waiting.
//
// Only the jobs named in allowed are started, matched exactly. A manual job is
// manual for a reason, and starting one because its name happened to contain
// "deploy" is a guess that puts a version into an environment nobody meant to
// touch. Everything else waiting is reported back and left for a person.
func (c *Client) PlayManualDeployJobs(ctx context.Context, projectPath string, pipeline int, allowed []string) (played, manual []string, err error) {
	jobs, err := c.pipelineJobs(ctx, projectPath, pipeline, "manual")
	if err != nil {
		return nil, nil, err
	}
	permitted := make(map[string]bool, len(allowed))
	for _, n := range allowed {
		permitted[n] = true
	}
	for _, j := range jobs {
		if permitted[j.Name] {
			if perr := c.playJob(ctx, projectPath, j.ID); perr == nil {
				played = append(played, j.Name)
				continue
			}
			// Refused by GitLab: still waiting, so say so. Reporting it as
			// started would be worse than reporting nothing.
		}
		manual = append(manual, j.Name)
	}
	return played, manual, nil
}

// PipelineStatus is the current state of a pipeline, for following it.
func (c *Client) PipelineStatus(ctx context.Context, projectPath string, id int) (status, webURL string, err error) {
	var p struct {
		Status string `json:"status"`
		WebURL string `json:"web_url"`
	}
	u := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d", c.base, url.PathEscape(projectPath), id)
	if err := c.getJSON(ctx, u, &p); err != nil {
		return "", "", err
	}
	return p.Status, p.WebURL, nil
}

// JobStatus is the current state of one job.
func (c *Client) JobStatus(ctx context.Context, projectPath string, id int) (status, webURL string, err error) {
	var j job
	u := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d", c.base, url.PathEscape(projectPath), id)
	if err := c.getJSON(ctx, u, &j); err != nil {
		return "", "", err
	}
	return j.Status, j.WebURL, nil
}

func (c *Client) pipelineJobs(ctx context.Context, projectPath string, pipeline int, scope string) ([]job, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs?per_page=100",
		c.base, url.PathEscape(projectPath), pipeline)
	if scope != "" {
		u += "&scope[]=" + url.QueryEscape(scope)
	}
	var jobs []job
	if err := c.getJSON(ctx, u, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (c *Client) playJob(ctx context.Context, projectPath string, id int) error {
	u := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/play", c.base, url.PathEscape(projectPath), id)
	resp, err := c.post(ctx, u, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("starting the job returned %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) post(ctx context.Context, u string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}
