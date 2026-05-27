package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const (
	defaultVPCCIDR    = "10.0.0.0/16"
	defaultSubnetCIDR = "10.0.1.0/24"
)

// VPCResource holds the IDs of a bootstrapped VPC and its associated resources.
type VPCResource struct {
	VPCID           string
	SubnetIDs       []string
	SecurityGroupID string
	name            string
	clients         *Clients
}

// NewVPC creates a VPC bootstrap resource with the given logical name.
func NewVPC(name string, clients *Clients) *VPCResource {
	return &VPCResource{
		name:    name,
		clients: clients,
	}
}

func (v *VPCResource) Name() string { return v.name }

func (v *VPCResource) Create(ctx context.Context) error {
	vpcOut, err := v.clients.EC2.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String(defaultVPCCIDR),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeVpc,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("ack-e2e-" + v.name)}},
		}},
	})
	if err != nil {
		return fmt.Errorf("creating VPC: %w", err)
	}
	v.VPCID = aws.ToString(vpcOut.Vpc.VpcId)

	// Wait for VPC to be available
	waiter := ec2.NewVpcAvailableWaiter(v.clients.EC2)
	if err := waiter.Wait(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{v.VPCID}}, 2*time.Minute); err != nil {
		return fmt.Errorf("waiting for VPC available: %w", err)
	}

	// Create a subnet
	subnetOut, err := v.clients.EC2.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     aws.String(v.VPCID),
		CidrBlock: aws.String(defaultSubnetCIDR),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSubnet,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("ack-e2e-" + v.name + "-subnet")}},
		}},
	})
	if err != nil {
		return fmt.Errorf("creating subnet: %w", err)
	}
	v.SubnetIDs = []string{aws.ToString(subnetOut.Subnet.SubnetId)}

	// Create a security group
	sgOut, err := v.clients.EC2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("ack-e2e-" + v.name + "-sg"),
		Description: aws.String("ACK e2e test security group"),
		VpcId:       aws.String(v.VPCID),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSecurityGroup,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("ack-e2e-" + v.name + "-sg")}},
		}},
	})
	if err != nil {
		return fmt.Errorf("creating security group: %w", err)
	}
	v.SecurityGroupID = aws.ToString(sgOut.GroupId)

	return nil
}

func (v *VPCResource) Delete(ctx context.Context) error {
	// Delete security group
	if v.SecurityGroupID != "" {
		_, _ = v.clients.EC2.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
			GroupId: aws.String(v.SecurityGroupID),
		})
	}

	// Delete subnets
	for _, subnetID := range v.SubnetIDs {
		_, _ = v.clients.EC2.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
			SubnetId: aws.String(subnetID),
		})
	}

	// Delete VPC
	if v.VPCID != "" {
		_, err := v.clients.EC2.DeleteVpc(ctx, &ec2.DeleteVpcInput{
			VpcId: aws.String(v.VPCID),
		})
		if err != nil {
			return fmt.Errorf("deleting VPC: %w", err)
		}
	}
	return nil
}
