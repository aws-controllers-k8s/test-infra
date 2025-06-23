package k8s

import (
	"fmt"
	"os"
	
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewClientFromConfig initializes a Kubernetes client from config
func NewClientFromConfig() (Client, error) {
	config, err := getKubernetesConfig()
	if err != nil {
		return nil, err
	}
	
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	
	return NewClient(client), nil
}

// GetKubernetesClientset returns a standard kubernetes clientset
func GetKubernetesClientset() (kubernetes.Interface, error) {
	config, err := getKubernetesConfig()
	if err != nil {
		return nil, err
	}
	
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	return client, nil
}

// getKubernetesConfig returns the Kubernetes configuration
func getKubernetesConfig() (*rest.Config, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = clientcmd.RecommendedHomeFile
		}
		
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to build kubernetes config: %w", err)
		}
	}
	
	return config, nil
}