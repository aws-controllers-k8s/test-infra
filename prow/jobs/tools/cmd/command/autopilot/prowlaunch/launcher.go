package prowlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

// jobAPI is what Launcher needs from the cluster (Kube; a fake in tests).
type jobAPI interface {
	Create(ctx context.Context, pj *ProwJob) error
	Get(ctx context.Context, name string) (*ProwJob, error)
	List(ctx context.Context, selector string) ([]ProwJob, error)
	Patch(ctx context.Context, name string, patch []byte) error
}

// Launcher is the real dispatcher.Launcher. Launch, Status and Abort are
// idempotent: the job's name is the launch id, so a repeated Launch finds
// the job it already created and adopts it.
type Launcher struct {
	cat  *Catalog
	cfg  Config
	jobs jobAPI
	art  Artifacts
}

var (
	_ dispatcher.Launcher  = (*Launcher)(nil)
	_ dispatcher.Inventory = (*Launcher)(nil)
)

// New is the launcher for cat's workflows.
func New(cat *Catalog, cfg Config, jobs jobAPI, art Artifacts) (*Launcher, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Launcher{cat: cat, cfg: cfg, jobs: jobs, art: art}, nil
}

func (l *Launcher) Shadow() bool { return false }

// Launch creates the job. If a job with its name exists, Launch adopts it
// when the Dispatcher made it from the same request, and fails otherwise.
func (l *Launcher) Launch(ctx context.Context, r dispatcher.LaunchRequest) error {
	pj, err := l.cat.Build(l.cfg, r)
	if err != nil {
		return err
	}
	err = l.jobs.Create(ctx, pj)
	if !errors.Is(err, ErrExists) {
		return err
	}
	got, err := l.jobs.Get(ctx, pj.Metadata.Name)
	if err != nil {
		return fmt.Errorf("adopt %s: %w", pj.Metadata.Name, err)
	}
	if !managed(got) || got.Metadata.Annotations[AnnotationRequest] != pj.Metadata.Annotations[AnnotationRequest] {
		return fmt.Errorf("ProwJob %s exists but was not made from this request; not adopting it", pj.Metadata.Name)
	}
	return nil
}

// Status maps the job's state to a phase and, for a job that succeeded or
// failed, reads its agent-result.json. A job that is missing, or that the
// Dispatcher did not make, is unknown: its result is never read.
func (l *Launcher) Status(ctx context.Context, id string) (dispatcher.Status, error) {
	pj, err := l.jobs.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return dispatcher.Status{Phase: dispatcher.PhaseUnknown}, nil
	}
	if err != nil {
		return dispatcher.Status{}, err
	}
	if !managed(pj) {
		return dispatcher.Status{Phase: dispatcher.PhaseUnknown}, nil
	}
	var job dispatcher.JobState
	switch pj.Status.State {
	case "", "scheduling", "triggered":
		return dispatcher.Status{Phase: dispatcher.PhasePending}, nil
	case "pending":
		return dispatcher.Status{Phase: dispatcher.PhaseRunning}, nil
	case "aborted":
		return dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobAborted}, nil
	case "error":
		return dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobErrored}, nil
	case "success":
		job = dispatcher.JobSucceeded
	case "failure":
		job = dispatcher.JobFailed
	default:
		return dispatcher.Status{Phase: dispatcher.PhaseUnknown}, nil
	}
	st := dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: job}
	key, ok := resultKey(pj)
	if !ok {
		st.Problem = "RESULT_UNLOCATABLE"
		return st, nil
	}
	data, err := l.art.Get(ctx, key, dispatcher.MaxResultBytes)
	switch {
	case errors.Is(err, ErrNotFound):
		st.Problem = "RESULT_MISSING"
		return st, nil
	case errors.Is(err, ErrTooLarge):
		st.Problem = "RESULT_TOO_LARGE"
		return st, nil
	case err != nil:
		// Transient (network, credentials): the next tick reads it again.
		return dispatcher.Status{}, err
	}
	rr, err := dispatcher.ParseResult(data, pj.Metadata.Annotations[AnnotationWorkItem])
	switch {
	case err != nil:
		st.Problem = "RESULT_INVALID"
	case rr.ProwJobID != pj.Metadata.Name:
		st.Problem = "RESULT_FOR_OTHER_JOB"
	case rr.Workflow != pj.Metadata.Labels[LabelWorkflow]:
		st.Problem = "RESULT_FOR_OTHER_WORKFLOW"
	default:
		st.Result = &rr
	}
	return st, nil
}

// Abort sets the job's state to aborted; Prow then deletes its pod. A job
// that is gone or already finished is left alone, and a job the Dispatcher
// did not make is never touched. The patch carries the resourceVersion it
// read, so it cannot overwrite a state Prow wrote in between.
func (l *Launcher) Abort(ctx context.Context, id string) error {
	for attempt := 0; attempt < 3; attempt++ {
		pj, err := l.jobs.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !managed(pj) {
			return fmt.Errorf("ProwJob %s was not made by the Dispatcher; not aborting it", id)
		}
		if terminal(pj.Status.State) {
			return nil
		}
		patch, err := json.Marshal(map[string]any{
			"metadata": map[string]any{"resourceVersion": pj.Metadata.ResourceVersion},
			"status":   map[string]any{"state": "aborted", "description": "Aborted by the ACK Autopilot Dispatcher"},
		})
		if err != nil {
			return err
		}
		err = l.jobs.Patch(ctx, id, patch)
		switch {
		case errors.Is(err, ErrConflict):
			continue
		case errors.Is(err, ErrNotFound):
			return nil
		}
		return err
	}
	return fmt.Errorf("abort %s: the job kept changing", id)
}

// Active lists the Dispatcher's jobs that have not finished.
func (l *Launcher) Active(ctx context.Context) ([]dispatcher.ActiveJob, error) {
	jobs, err := l.jobs.List(ctx, LabelManagedBy+"="+ManagedBy)
	if err != nil {
		return nil, err
	}
	var out []dispatcher.ActiveJob
	for i := range jobs {
		pj := &jobs[i]
		if !managed(pj) || terminal(pj.Status.State) {
			continue
		}
		out = append(out, dispatcher.ActiveJob{
			LaunchID: pj.Metadata.Name,
			Workflow: pj.Metadata.Labels[LabelWorkflow],
			Service:  pj.Metadata.Labels[LabelService],
			Refresh:  strings.HasPrefix(pj.Metadata.Labels[LabelRun], "f"),
		})
	}
	return out, nil
}

// managed reports whether the Dispatcher made the job: the label, a launch
// id for a name, and an allowlisted workflow.
func managed(pj *ProwJob) bool {
	_, allowed := Allowed[pj.Metadata.Labels[LabelWorkflow]]
	return pj.Metadata.Labels[LabelManagedBy] == ManagedBy && launchIDRE.MatchString(pj.Metadata.Name) && allowed
}

func terminal(state string) bool {
	switch state {
	case "success", "failure", "aborted", "error":
		return true
	}
	return false
}

var buildIDRE = regexp.MustCompile(`^[0-9]{1,20}$`)

// resultKey is where Prow's sidecar uploads a periodic's artifacts
// (path_strategy explicit): logs/<job>/<build id>/artifacts/. The job name
// must be the one the workflow runs as.
func resultKey(pj *ProwJob) (string, bool) {
	job := Allowed[pj.Metadata.Labels[LabelWorkflow]]
	if job == "" || pj.Spec.Job != job || !buildIDRE.MatchString(pj.Status.BuildID) {
		return "", false
	}
	return "logs/" + job + "/" + pj.Status.BuildID + "/artifacts/agent-result.json", true
}
