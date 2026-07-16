package e2e

import (
	"fmt"
	"os"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	k8sClient client.Client
	clientMu  sync.Mutex
	scheme    *runtime.Scheme
)

// SetScheme sets the runtime scheme used to build the K8s client. Controllers
// must call this in TestMain before Setup() to register their CRD types.
func SetScheme(s *runtime.Scheme) {
	scheme = s
}

// Setup initializes the K8s client for e2e tests. It reads kubeconfig from the
// KUBECONFIG env var or the default ~/.kube/config location.
func Setup() {
	clientMu.Lock()
	defer clientMu.Unlock()

	if k8sClient != nil {
		return
	}
	if scheme == nil {
		panic("e2e.SetScheme() must be called before e2e.Setup()")
	}

	cfg, err := loadKubeConfig()
	if err != nil {
		panic(fmt.Sprintf("failed to load kubeconfig: %v", err))
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(fmt.Sprintf("failed to create k8s client: %v", err))
	}
	k8sClient = c
}

// Teardown performs any cleanup needed after tests complete.
func Teardown() {
	// Currently a no-op; reserved for future use (e.g., closing connections).
}

// Client returns the initialized K8s client. Panics if Setup() hasn't been called.
func Client() client.Client {
	if k8sClient == nil {
		panic("e2e.Setup() must be called before e2e.Client()")
	}
	return k8sClient
}

func loadKubeConfig() (*rest.Config, error) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = home + "/.kube/config"
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}
