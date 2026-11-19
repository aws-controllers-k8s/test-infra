package bootstrap

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

const assumeRolePolicyDocument = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {"Service": "lambda.amazonaws.com"},
      "Action": "sts:AssumeRole"
    }
  ]
}`

// IAMRoleResource holds the ARN of a bootstrapped IAM role.
type IAMRoleResource struct {
	RoleARN  string
	RoleName string
	name     string
	clients  *Clients
}

// NewIAMRole creates an IAM role bootstrap resource with the given logical name.
func NewIAMRole(name string, clients *Clients) *IAMRoleResource {
	return &IAMRoleResource{
		name:    name,
		clients: clients,
	}
}

func (r *IAMRoleResource) Name() string { return r.name }

func (r *IAMRoleResource) Create(ctx context.Context) error {
	roleName := "ack-e2e-" + r.name
	out, err := r.clients.IAM.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(assumeRolePolicyDocument),
		Tags: []iamtypes.Tag{{
			Key:   aws.String("Purpose"),
			Value: aws.String("ack-e2e-test"),
		}},
	})
	if err != nil {
		return fmt.Errorf("creating IAM role: %w", err)
	}
	r.RoleName = roleName
	r.RoleARN = aws.ToString(out.Role.Arn)
	return nil
}

func (r *IAMRoleResource) Delete(ctx context.Context) error {
	if r.RoleName == "" {
		return nil
	}
	_, err := r.clients.IAM.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: aws.String(r.RoleName),
	})
	if err != nil {
		return fmt.Errorf("deleting IAM role: %w", err)
	}
	return nil
}
