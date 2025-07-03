package webhook

import (
	k8sclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/k8s"
)

// submitProwJob submits a ProwJob to the Kubernetes cluster
func (s *Server) submitProwJob(prowJob *k8sclient.ProwJob) error {
	ctx, cancel := ContextWithDefaultTimeout()
	defer cancel()
	return s.k8sProwClient.SubmitProwJob(ctx, prowJob, s.prowJobNamespace)
}
