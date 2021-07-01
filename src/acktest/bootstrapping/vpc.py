from typing import List, Union
import boto3
import logging

from dataclasses import dataclass, field

from . import Bootstrappable
from .. import resources
from ..aws.identity import get_region

# Subnets inside the default VPC CIDR block will be of form 10.0.*.0/24
VPC_CIDR_BLOCK = "10.0.0.0/16"


@dataclass
class VPC(Bootstrappable):
    # Inputs
    name_prefix: Union[str, None] = field(default=None)
    public_subnets: int = 2
    private_subnets: int = 0

    vpc_cidr_block: str = field(default=VPC_CIDR_BLOCK)
    public_subnet_cidr_blocks: Union[List[str], None] = field(default=None)
    private_subnet_cidr_blocks: Union[List[str], None] = field(default=None)

    # Outputs
    name: Union[str, None] = field(default=None, init=False)
    vpc_id: str = field(init=False)

    internet_gateway_id: str = field(init=False)
    public_route_table_id: str = field(init=False)
    private_route_table_id: str = field(init=False)
    public_subnet_ids: List[str] = field(init=False, default_factory=list)
    private_subnet_ids: List[str] = field(init=False, default_factory=list)
    
    @property
    def ec2_client(self):
        return boto3.client("ec2", region_name=self.region)

    @property
    def ec2_resource(self):
        return boto3.resource("ec2", region_name=self.region)


    def bootstrap(self):
        """Creates a VPC with an auto-generated name and any number of public
           and private subnets.
        """
        vpc = self.ec2_client.create_vpc(CidrBlock=self.vpc_cidr_block)

        self.vpc_id = vpc['Vpc']['VpcId']
        logging.info(f"Created VPC {self.vpc_id}")

        if self.name_prefix is not None:
            self.name = resources.random_suffix_name(self.name_prefix, 63)
            self.ec2_client.create_tags(Resources=[self.vpc_id], Tags=[{'Key': 'Name', 'Value': self.name}])

        vpc = self.ec2_resource.Vpc(self.vpc_id)
        vpc.wait_until_available()

        self._create_internet_gateway()
        public_route_table = self._create_public_route_table()
        self.public_route_table_id = public_route_table.id

        region_azs = self.get_availability_zone_names()

        subnet_count = 0
        for i in range(self.public_subnets):
            if self.public_subnet_cidr_blocks is None:
                cidr_block = f"10.0.{subnet_count}.0/24"
            else:
                cidr_block = self.public_subnet_cidr_blocks[i]
            subnet = vpc.create_subnet(CidrBlock=cidr_block, AvailabilityZone=region_azs[subnet_count % len(region_azs)])
            self.public_subnet_ids.append(subnet.id)

            self.ec2_client.associate_route_table(RouteTableId=public_route_table.id, SubnetId=subnet.id)

            logging.info(f"Created public subnet {subnet.id}")
            subnet_count += 1

        if self.private_subnets == 0:
            return

        private_route_table = self._create_private_route_table()
        self.private_route_table_id = private_route_table.id

        for i in range(self.private_subnets):
            if self.private_subnet_cidr_blocks is None:
                cidr_block = f"10.0.{subnet_count}.0/24"
            else:
                cidr_block = self.private_subnet_cidr_blocks[i]
            subnet = vpc.create_subnet(CidrBlock=cidr_block, AvailabilityZone=region_azs[subnet_count % len(region_azs)])
            self.private_subnet_ids.append(subnet.id)

            self.ec2_client.associate_route_table(RouteTableId=private_route_table.id, SubnetId=subnet.id)

            logging.info(f"Created private subnet {subnet.id}")
            subnet_count += 1

    def cleanup(self):
        """Deletes a VPC.
        """
        vpc = self.ec2_resource.Vpc(self.vpc_id)

        for subnet in self.public_subnet_ids + self.private_subnet_ids:
            self.ec2_client.delete_subnet(SubnetId=subnet)

            logging.info(f"Deleted subnet {self.name}")

        self.ec2_client.delete_route_table(RouteTableId=self.private_route_table_id)
        self.ec2_client.delete_route_table(RouteTableId=self.public_route_table_id)

        vpc.detach_internet_gateway(InternetGatewayId=self.internet_gateway_id)
        self.ec2_client.delete_internet_gateway(InternetGatewayId=self.internet_gateway_id)

        vpc.delete()

        logging.info(f"Deleted VPC {self.name}")

    def get_availability_zone_names(self):
        zones = self.ec2_client.describe_availability_zones()
        return list(map(lambda x: x['ZoneName'], zones['AvailabilityZones']))

    @property
    def route_tables(self):
        return self.ec2_resource.route_tables.filter(Filters=[{'Name': 'vpc-id', 'Values': [self.vpc_id]}])

    def _get_public_route_table(self):
        """Gets the public route table in the VPC if it exists, else None.
        """
        for route_table in self.route_tables:
            for ra in route_table.routes_attribute:
                if ra.get('DestinationCidrBlock') == '0.0.0.0/0' and ra.get('GatewayId') is not None:
                    return ra
        return None

    def _get_private_route_table(self):
        """Gets the private route table in the VPC if it exists, else None.
        """
        for route_table in self.route_tables:
            for ra in route_table.routes_attribute:
                if ra.get('DestinationCidrBlock') == '0.0.0.0/0' and ra.get('GatewayId') is None:
                    return ra
        return None

    def _create_internet_gateway(self):
        """Creates a private route table for the VPC.
        """
        vpc = self.ec2_resource.Vpc(self.vpc_id)

        internet_gateway = self.ec2_resource.create_internet_gateway()
        vpc.attach_internet_gateway(InternetGatewayId=internet_gateway.id)

        self.internet_gateway_id = internet_gateway.id

    def _create_public_route_table(self):
        """Creates a private route table for the VPC.
        """
        vpc = self.ec2_resource.Vpc(self.vpc_id)

        route_table = vpc.create_route_table()
        route_table.create_route(DestinationCidrBlock='0.0.0.0/0', GatewayId=self.internet_gateway_id)

        return route_table
    
    def _create_private_route_table(self):
        """Creates a private route table for the VPC.
        """
        vpc = self.ec2_resource.Vpc(self.vpc_id)

        route_table = vpc.create_route_table()

        return route_table