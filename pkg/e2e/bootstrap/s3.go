package bootstrap

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3BucketResource holds the name of a bootstrapped S3 bucket.
type S3BucketResource struct {
	BucketName string
	name       string
	clients    *Clients
}

// NewS3Bucket creates an S3 bucket bootstrap resource with the given logical name.
func NewS3Bucket(name string, clients *Clients) *S3BucketResource {
	return &S3BucketResource{
		name:    name,
		clients: clients,
	}
}

func (b *S3BucketResource) Name() string { return b.name }

func (b *S3BucketResource) Create(ctx context.Context) error {
	bucketName := "ack-e2e-" + b.name
	_, err := b.clients.S3.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		return fmt.Errorf("creating S3 bucket: %w", err)
	}
	b.BucketName = bucketName
	return nil
}

func (b *S3BucketResource) Delete(ctx context.Context) error {
	if b.BucketName == "" {
		return nil
	}
	_, err := b.clients.S3.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(b.BucketName),
	})
	if err != nil {
		return fmt.Errorf("deleting S3 bucket: %w", err)
	}
	return nil
}
