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
import time

import boto3

from dataclasses import dataclass, field

from .. import resources
from . import Bootstrappable, VPC


@dataclass
class NetworkLoadBalancer(Bootstrappable):
  # Inputs
  name_prefix: str
  ip_address_type: str = "ipv4"
  type: str = "network"
  scheme: str = "internet-facing"

  # Subresources

  # Outputs
  arn: str = field(init=False)

  @property
  def elbv2_client(self):
    return boto3.client("elbv2", region_name=self.region)

  @property
  def elbv2_resource(self):
    return boto3.resource("elbv2", region_name=self.region)

  def bootstrap(self):
    """Creates a Network Load Balancer cluster with an auto-generated name.
    """
    super().bootstrap()

    self.name = resources.random_suffix_name(self.name_prefix, 32)
    test_vpc = VPC(name_prefix="test_vpc", num_public_subnet=2, num_private_subnet=0)
    test_vpc.bootstrap()

    network_load_balancer = self.elbv2_client.create_load_balancer(
      Name=self.name,
      Scheme=self.scheme,
      Type=self.type,
      IpAddressType=self.ip_address_type,
      Subnets=test_vpc.public_subnets.subnet_ids
    )

    self.arn = network_load_balancer.get("LoadBalancers")[0].get("LoadBalancerArn")

    waiter = self.elbv2_client.get_waiter('load_balancer_available')
    waiter.wait(LoadBalancerArns=[self.arn])

  def cleanup(self):
    """Deletes a Network Load Balancer.
    """

    self.elbv2_client.delete_load_balancer(
      LoadBalancerArn=self.arn
    )

    waiter = self.elbv2_client.get_waiter('load_balancers_deleted')
    waiter.wait(LoadBalancerArns=[self.arn])

    super().cleanup()