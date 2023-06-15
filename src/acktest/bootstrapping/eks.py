# Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may
# not use this file except in compliance with the License. A copy of the
# License is located at
#
#	 http://aws.amazon.com/apache2.0/
#
# or in the "license" file accompanying this file. This file is distributed
# on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
# express or implied. See the License for the specific language governing
# permissions and limitations under the License.

import boto3

from dataclasses import dataclass, field
from typing import Union

from .. import resources
from . import Bootstrappable, BootstrapFailureException
from .vpc import VPC
from .iam import Role


@dataclass
class Cluster(Bootstrappable):
    # Inputs
    name_prefix: str
    num_managed_nodes: int = 2
    node_instance: str = "m5.xlarge"

    # Subresources
    vpc: VPC = field(init=False, default=None)
    cluster_role: Role = field(init=False, default=None)
    node_role: Role = field(init=False, default=None)

    # Outputs
    name: Union[str, None] = field(default=None, init=False)
    nodegroup_name: Union[str, None] = field(default=None, init=False)

    def __post_init__(self):
        self.vpc = VPC(f'{self.name_prefix}-vpc')
        self.cluster_role = Role(f'{self.name_prefix}-cluster-role', "eks.amazonaws.com", managed_policies=["arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"])
        self.node_role = Role(f'{self.name_prefix}-nodegroup-role', "ec2.amazonaws.com", managed_policies=[
            "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
            "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
            "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
        ])

    @property
    def eks_client(self):
        return boto3.client("eks", region_name=self.region)

    @property
    def eks_resource(self):
        return boto3.resource("eks", region_name=self.region)

    def bootstrap(self):
        """Creates an EKS cluster with an auto-generated name on a separate VPC.
        """
        super().bootstrap()

        self.name = resources.random_suffix_name(self.name_prefix, 63)
        self.nodegroup_name = resources.random_suffix_name(f'{self.name_prefix}-ng', 63)

        cluster = self.eks_client.create_cluster(
            name=self.name,
            roleArn=self.cluster_role.arn,
            resourcesVpcConfig={
                "subnetIds": self.vpc.public_subnets.subnet_ids
            }
        )

        waiter = self.eks_client.get_waiter('cluster_active')
        waiter.wait(name=self.name)

        nodegroup = self.eks_client.create_nodegroup(
            clusterName=self.name,
            nodegroupName=self.nodegroup_name,
            scalingConfig={
                "minSize": self.num_managed_nodes,
                "maxSize": self.num_managed_nodes,
                "desiredSize": self.num_managed_nodes,
            },
            subnets=self.vpc.public_subnets.subnet_ids,
            instanceTypes=[self.node_instance],
            nodeRole=self.node_role.arn,
        )

        waiter = self.eks_client.get_waiter('nodegroup_active')
        waiter.wait(clusterName=self.name, nodegroupName=self.nodegroup_name)

    def cleanup(self):
        """Deletes an EKS cluster an all associated resources.
        """
        self.eks_client.delete_nodegroup(
            clusterName=self.name,
            nodegroupName=self.nodegroup_name,
        )

        waiter = self.eks_client.get_waiter('nodegroup_deleted')
        waiter.wait(clusterName=self.name, nodegroupName=self.nodegroup_name)

        self.eks_client.delete_cluster(
            name=self.name
        )

        waiter = self.eks_client.get_waiter('cluster_deleted')
        err = waiter.wait(
            name=self.name,
            WaiterConfig={
                'Delay': 30,
                'MaxAttempts': 100
            }
        )

        if err is not None:
            raise BootstrapFailureException(err)

        super().cleanup()