package k8s

import (
	"context"
	"time"
)

// DefaultTimeout is the standard timeout for API operations
const DefaultTimeout = 30 * time.Second

// ContextWithDefaultTimeout returns a new context with the default timeout
func ContextWithDefaultTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), DefaultTimeout)
}