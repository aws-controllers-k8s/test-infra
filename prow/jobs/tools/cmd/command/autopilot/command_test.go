package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/findingsblock"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/issueregion"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const (
	now     = "2026-11-11T08:30:00Z"
	modeKey = "api-change|s3|Bucket|spec:Mode|needs-config"
	bumpKey = "sdk-bump|s3|*|service/s3/v1.97.3"
)

// issueBody is a detector issue body: prose outside the generated region and
// a findings block inside it, as issuefinder/ingest_test.go builds them.
func issueBody(t *testing.T) string {
	t.Helper()
	block, omitted := findingsblock.Format(
		findingsblock.Header{Service: "s3", From: "v1.41.5", To: "service/s3/v1.97.3"},
		[]findingsblock.Line{{Class: "spec-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"}},
		1<<16)
	require.Zero(t, omitted)
	return "maintainer note\n" + issueregion.Begin + "\nprose\n" + block + issueregion.End + "\n"
}

func newClient(t *testing.T, h http.Handler) *github.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := github.NewClient(nil)
	base, err := url.Parse(srv.URL + "/")
	require.NoError(t, err)
	c.BaseURL = base
	return c
}

// fakeGitHub serves one bot issue (#27) and one stranger's issue (#28) that
// Ingest must skip.
func fakeGitHub(t *testing.T) *github.Client {
	body, err := json.Marshal(issueBody(t))
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/ack-private/issues", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		fmt.Fprintf(w, `[{"number":27,"html_url":"https://x/27","state":"open","user":{"login":"ack-bot"},
		  "labels":[{"name":"ack/api-change-detected"},{"name":"service/s3"}],"body":%s},
		  {"number":28,"html_url":"https://x/28","state":"open","user":{"login":"someone"},
		  "labels":[{"name":"ack/api-change-detected"},{"name":"service/s3"}],"body":%s}]`, body, body)
	})
	for _, n := range []int{27, 28} {
		mux.HandleFunc(fmt.Sprintf("/repos/o/ack-private/issues/%d/comments", n), func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[]`)
		})
	}
	mux.HandleFunc("/orgs/o/teams/ack-admin/members", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	return newClient(t, mux)
}

func opts(dir string) ingestOptions {
	return ingestOptions{Owner: "o", Repo: "ack-private", BotLogin: "ack-bot", TeamSlug: "ack-admin", LedgerDir: dir}
}

func TestIngestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	o := opts(dir)
	o.DryRun = true
	require.NoError(t, ingest(context.Background(), fakeGitHub(t), o, now, &out))
	assert.Contains(t, out.String(), "new "+ledger.KeyHash(modeKey)+" DETECTED "+modeKey+"\n")
	assert.Contains(t, out.String(), "new "+ledger.KeyHash(bumpKey)+" DETECTED "+bumpKey+"\n")
	assert.Contains(t, out.String(), `skipped #28: author "someone" is not "ack-bot"`)
	assert.Contains(t, out.String(), "2 issue(s) read, 1 skipped, 2 item(s) changed")
	assert.Contains(t, out.String(), "dry run")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestIngestWritesLintCleanLedger(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, ingest(context.Background(), fakeGitHub(t), opts(dir), now, &out))
	assert.NotContains(t, out.String(), "dry run")
	files, err := filepath.Glob(filepath.Join(dir, "workitems", "open", "*.json"))
	require.NoError(t, err)
	assert.Len(t, files, 2)
	assert.Empty(t, ledger.Lint(dir))

	// A second run changes nothing.
	out.Reset()
	require.NoError(t, ingest(context.Background(), fakeGitHub(t), opts(dir), "2026-11-11T20:30:00Z", &out))
	assert.Contains(t, out.String(), "2 issue(s) read, 1 skipped, 0 item(s) changed")
}

func TestIngestFailsWhenLintFails(t *testing.T) {
	// An item Load accepts but Lint rejects: its schema is wrong.
	dir := t.TempDir()
	bad := &ledger.WorkItem{
		Schema: "bogus", Key: "api-change|s3|Other|new-resource", Source: "api-change", Service: "s3",
		History: []ledger.Transition{{TS: now, To: ledger.Detected, By: "test"}}, State: ledger.Detected,
	}
	bad.KeyHash = ledger.KeyHash(bad.Key)
	require.NoError(t, ledger.Save(dir, map[string]*ledger.WorkItem{bad.Key: bad}))

	var out bytes.Buffer
	err := ingest(context.Background(), fakeGitHub(t), opts(dir), now, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not lint clean")
	assert.Contains(t, err.Error(), "schema")
}

func TestIngestRejectsMissingOptions(t *testing.T) {
	// No request may be made before the options are checked.
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
	}))
	for name, mutate := range map[string]func(*ingestOptions){
		"bot-login":    func(o *ingestOptions) { o.BotLogin = "" },
		"team-slug":    func(o *ingestOptions) { o.TeamSlug = " " },
		"ledger-dir":   func(o *ingestOptions) { o.LedgerDir = "" },
		"issues-owner": func(o *ingestOptions) { o.Owner = "" },
		"issues-repo":  func(o *ingestOptions) { o.Repo = "" },
	} {
		t.Run(name, func(t *testing.T) {
			o := opts(t.TempDir())
			mutate(&o)
			err := ingest(context.Background(), client, o, now, &bytes.Buffer{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--"+name)
		})
	}
}

func TestIngestRequiresToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	Command.SetArgs([]string{"ingest-issues", "--bot-login", "ack-bot", "--team-slug", "t", "--ledger-dir", t.TempDir()})
	Command.SetOut(&bytes.Buffer{})
	Command.SetErr(&bytes.Buffer{})
	err := Command.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GITHUB_TOKEN")
}

func TestIngestCommandRejectsEmptyBotLogin(t *testing.T) {
	// The flag is given, so MarkFlagRequired is satisfied; it is still empty.
	t.Setenv("GITHUB_TOKEN", "unused")
	Command.SetArgs([]string{"ingest-issues", "--bot-login", "", "--team-slug", "t", "--ledger-dir", t.TempDir()})
	Command.SetOut(&bytes.Buffer{})
	Command.SetErr(&bytes.Buffer{})
	err := Command.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--bot-login must not be empty")
}

func TestLintLedgerCommand(t *testing.T) {
	run := func(dir string) (string, error) {
		var stderr bytes.Buffer
		Command.SetArgs([]string{"lint-ledger", "--ledger-dir", dir})
		Command.SetOut(&bytes.Buffer{})
		Command.SetErr(&stderr)
		err := Command.Execute()
		return stderr.String(), err
	}

	dir := t.TempDir()
	require.NoError(t, ingest(context.Background(), fakeGitHub(t), opts(dir), now, &bytes.Buffer{}))
	_, err := run(dir)
	assert.NoError(t, err)

	stray := filepath.Join(dir, "workitems", "open", "notes.txt")
	require.NoError(t, os.WriteFile(stray, []byte("x"), 0o644))
	stderr, err := run(dir)
	require.Error(t, err)
	assert.Contains(t, stderr, stray)
}
