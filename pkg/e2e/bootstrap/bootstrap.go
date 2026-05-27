package bootstrap

import (
	"context"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Resource is the interface that all bootstrap resources implement.
type Resource interface {
	// Create provisions the AWS resource. It should be idempotent.
	Create(ctx context.Context) error
	// Delete removes the AWS resource.
	Delete(ctx context.Context) error
	// Name returns the logical name of this bootstrap resource.
	Name() string
}

// Clients holds AWS SDK clients shared across bootstrap resources.
type Clients struct {
	EC2 *ec2.Client
	IAM *iam.Client
	S3  *s3.Client
}

// NewClients creates AWS SDK clients using the default credential chain.
func NewClients(ctx context.Context) (*Clients, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return &Clients{
		EC2: ec2.NewFromConfig(cfg),
		IAM: iam.NewFromConfig(cfg),
		S3:  s3.NewFromConfig(cfg),
	}, nil
}

// SetupAll creates bootstrap resources in order (respecting dependencies).
// Resources are created sequentially since they may depend on each other.
func SetupAll(ctx context.Context, resources []Resource) error {
	for _, r := range resources {
		log.Printf("[bootstrap] creating %s...", r.Name())
		if err := r.Create(ctx); err != nil {
			return fmt.Errorf("creating bootstrap resource %s: %w", r.Name(), err)
		}
		log.Printf("[bootstrap] %s created", r.Name())
	}
	return nil
}

// TeardownAll deletes bootstrap resources in reverse order.
func TeardownAll(ctx context.Context, resources []Resource) {
	for i := len(resources) - 1; i >= 0; i-- {
		r := resources[i]
		log.Printf("[bootstrap] deleting %s...", r.Name())
		if err := r.Delete(ctx); err != nil {
			log.Printf("[bootstrap] WARNING: failed to delete %s: %v", r.Name(), err)
		} else {
			log.Printf("[bootstrap] %s deleted", r.Name())
		}
	}
}
