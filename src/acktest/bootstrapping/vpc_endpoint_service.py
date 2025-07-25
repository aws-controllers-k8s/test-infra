import boto3

from dataclasses import dataclass, field
from acktest.bootstrapping.elbv2 import NetworkLoadBalancer
from . import Bootstrappable
from .. import resources

@dataclass
class VpcEndpointServiceConfiguration(Bootstrappable):
    # Inputs
    name_prefix: str
    name: str = field(init=False, default=None)

    # Subresources
    networkLoadBalancer: NetworkLoadBalancer = field(init=False, default=None)

    # Outputs
    service_id: str = field(init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 24)
        self.networkLoadBalancer = NetworkLoadBalancer(f"nlb-{self.name_prefix}")

    @property
    def ec2_client(self):
        return boto3.client("ec2", region_name=self.region)

    @property
    def ec2_resource(self):
        return boto3.resource("ec2", region_name=self.region)

    def bootstrap(self):
        super().bootstrap()
        
        vpc_endpoint_service = self.ec2_client.create_vpc_endpoint_service_configuration(
            AcceptanceRequired=True,
            DryRun=False,
            NetworkLoadBalancerArns=[self.networkLoadBalancer.arn],
            TagSpecifications=[{
                "ResourceType": "vpc-endpoint-service",
                "Tags": [
                    {
                        "Key": "Name",
                        "Value": self.name
                    }
                ]
            }]
        )

        self.service_id = vpc_endpoint_service["ServiceConfiguration"]["ServiceId"]

    
    def cleanup(self):
        if hasattr(self, "service_id"):
            self.ec2_client.delete_vpc_endpoint_service_configurations(
                ServiceIds=[self.service_id]
            )
        return super().cleanup()