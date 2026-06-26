package e2e

import "os"

const (
	defaultNamespace = "default"
)

// Namespace returns the K8s namespace where test resources are created.
// Set ACK_TEST_NAMESPACE to override the default.
func Namespace() string {
	if ns := os.Getenv("ACK_TEST_NAMESPACE"); ns != "" {
		return ns
	}
	return defaultNamespace
}

// Region returns the AWS region for test execution.
// Set AWS_REGION or AWS_DEFAULT_REGION.
func Region() string {
	if r := os.Getenv("AWS_REGION"); r != "" {
		return r
	}
	return os.Getenv("AWS_DEFAULT_REGION")
}

// AccountID returns the AWS account ID for test execution.
func AccountID() string {
	return os.Getenv("AWS_ACCOUNT_ID")
}
