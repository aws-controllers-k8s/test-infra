package prowlaunch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Errors the API calls map status codes to.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrConflict = errors.New("conflict")
)

// serviceAccountDir is where Kubernetes mounts the pod's ServiceAccount
// token and the cluster CA.
const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

const (
	kubeTimeout   = 30 * time.Second
	maxKubeBody   = 8 << 20
	listPageSize  = 100
	maxListPages  = 20
	maxErrMessage = 200
)

var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Kube is a minimal client for ProwJobs in one namespace of the cluster the
// Dispatcher runs in: create, get, list by label and merge-patch, nothing
// else. It authenticates with the pod's ServiceAccount token, read again for
// every request because bound tokens rotate, verifies the API server against
// the cluster CA, never uses a proxy, and opens no listener.
type Kube struct {
	base      string // .../apis/prow.k8s.io/v1/namespaces/<ns>/prowjobs
	tokenFile string
	client    *http.Client
}

// InCluster is the client for the pod's own cluster.
func InCluster(namespace string, getenv func(string) string) (*Kube, error) {
	host, port := getenv("KUBERNETES_SERVICE_HOST"), getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not in a cluster: KUBERNETES_SERVICE_HOST or KUBERNETES_SERVICE_PORT is unset")
	}
	ca, err := os.ReadFile(filepath.Join(serviceAccountDir, "ca.crt"))
	if err != nil {
		return nil, err
	}
	return newKube("https://"+net.JoinHostPort(host, port), namespace, filepath.Join(serviceAccountDir, "token"), ca)
}

func newKube(server, namespace, tokenFile string, caPEM []byte) (*Kube, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no CA certificate in ca.crt")
	}
	tr := &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return newKubeWith(server, namespace, tokenFile, &http.Client{Transport: tr, Timeout: kubeTimeout})
}

func newKubeWith(server, namespace, tokenFile string, client *http.Client) (*Kube, error) {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		return nil, fmt.Errorf("API server %q is not https://host:port", server)
	}
	if !nameRE.MatchString(namespace) {
		return nil, fmt.Errorf("namespace %q is not a Kubernetes name", namespace)
	}
	return &Kube{base: server + "/apis/prow.k8s.io/v1/namespaces/" + namespace + "/prowjobs", tokenFile: tokenFile, client: client}, nil
}

// Create creates pj. ErrExists if a job with its name exists.
func (k *Kube) Create(ctx context.Context, pj *ProwJob) error {
	b, err := json.Marshal(pj)
	if err != nil {
		return err
	}
	code, _, err := k.do(ctx, http.MethodPost, k.base, b, "application/json")
	switch {
	case err != nil:
		return err
	case code == http.StatusConflict:
		return fmt.Errorf("create %s: %w", pj.Metadata.Name, ErrExists)
	}
	return nil
}

// Get reads one job. ErrNotFound if there is none.
func (k *Kube) Get(ctx context.Context, name string) (*ProwJob, error) {
	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("%q is not a ProwJob name", name)
	}
	code, body, err := k.do(ctx, http.MethodGet, k.base+"/"+name, nil, "")
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, fmt.Errorf("get %s: %w", name, ErrNotFound)
	}
	var pj ProwJob
	if err := json.Unmarshal(body, &pj); err != nil {
		return nil, fmt.Errorf("get %s: %w", name, err)
	}
	return &pj, nil
}

// List reads every job matching a label selector, a page at a time.
func (k *Kube) List(ctx context.Context, selector string) ([]ProwJob, error) {
	var out []ProwJob
	cont := ""
	for page := 0; page < maxListPages; page++ {
		q := url.Values{"labelSelector": {selector}, "limit": {fmt.Sprint(listPageSize)}}
		if cont != "" {
			q.Set("continue", cont)
		}
		_, body, err := k.do(ctx, http.MethodGet, k.base+"?"+q.Encode(), nil, "")
		if err != nil {
			return nil, err
		}
		var l struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []ProwJob `json:"items"`
		}
		if err := json.Unmarshal(body, &l); err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		out = append(out, l.Items...)
		if cont = l.Metadata.Continue; cont == "" {
			return out, nil
		}
	}
	return nil, fmt.Errorf("list: more than %d pages", maxListPages)
}

// Patch applies a JSON merge patch. ErrNotFound if the job is gone,
// ErrConflict if the patch's resourceVersion is stale.
func (k *Kube) Patch(ctx context.Context, name string, patch []byte) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%q is not a ProwJob name", name)
	}
	code, _, err := k.do(ctx, http.MethodPatch, k.base+"/"+name, patch, "application/merge-patch+json")
	switch {
	case err != nil:
		return err
	case code == http.StatusNotFound:
		return fmt.Errorf("patch %s: %w", name, ErrNotFound)
	case code == http.StatusConflict:
		return fmt.Errorf("patch %s: %w", name, ErrConflict)
	}
	return nil
}

// do sends one request. It returns the status code for 2xx, 404 and 409 and
// an error for anything else. Errors never hold the token.
func (k *Kube) do(ctx context.Context, method, u string, body []byte, contentType string) (int, []byte, error) {
	tok, err := os.ReadFile(k.tokenFile)
	if err != nil {
		return 0, nil, fmt.Errorf("read the ServiceAccount token: %w", err)
	}
	token := strings.TrimSpace(string(tok))
	if token == "" {
		return 0, nil, errors.New("the ServiceAccount token is empty")
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s prowjobs: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxKubeBody+1))
	if err != nil {
		return 0, nil, fmt.Errorf("%s prowjobs: %w", method, err)
	}
	if len(data) > maxKubeBody {
		return 0, nil, fmt.Errorf("%s prowjobs: response larger than %d bytes", method, maxKubeBody)
	}
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300, c == http.StatusNotFound, c == http.StatusConflict:
		return c, data, nil
	}
	var st struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &st)
	msg := st.Message
	if len(msg) > maxErrMessage {
		msg = msg[:maxErrMessage] + "..."
	}
	return 0, nil, fmt.Errorf("%s prowjobs: %d %s: %s", method, resp.StatusCode, st.Reason, msg)
}
