from typing import List, Union
import boto3

from dataclasses import dataclass, field

from . import BootstrapFailureException, Bootstrappable
from .. import resources

# Subnets inside the default VPC CIDR block will be of form 10.0.*.0/24
VPC_CIDR_BLOCK = "10.0.0.0/16"

@dataclass
class InternetGateway(Bootstrappable):
    # Inputs
    vpc_id: str

    # Outputs
    internet_gateway_id: str = field(init=False)

    @property
    def ec2_client(self):
        return boto3.client("ec2", region_name=self.region)

    @property
    def ec2_resource(self):
        return boto3.resource("ec2", region_name=self.region)

    def bootstrap(self):
        """Creates an internet gateway.
        """
        vpc = self.ec2_resource.Vpc(self.vpc_id)

        internet_gateway = self.ec2_resource.create_internet_gateway()
        vpc.attach_internet_gateway(InternetGatewayId=internet_gateway.id)

        self.internet_gateway_id = internet_gateway.id

    def cleanup(self):
        """Deletes an internet gateway.
        """
        vpc = self.ec2_resource.Vpc(self.vpc_id)

        vpc.detach_internet_gateway(InternetGatewayId=self.internet_gateway_id)
        self.ec2_client.delete_internet_gateway(InternetGatewayId=self.internet_gateway_id)

@dataclass
class RouteTable(Bootstrappable):
    # Inputs
    vpc_id: str
    is_public: bool = False

    # Subresources
    internet_gateway: InternetGateway = field(init=False, default=None)

    # Outputs
    route_table_id: str = field(init=False)

    def __post_init__(self):
        if self.is_public:
            self.internet_gateway = InternetGateway(self.vpc_id)

    @property
    def ec2_client(self):
        return boto3.client("ec2", region_name=self.region)

    @property
    def ec2_resource(self):
        return boto3.resource("ec2", region_name=self.region)

    def bootstrap(self):
        """Creates a route table.
        """
        super().bootstrap()

        vpc = self.ec2_resource.Vpc(self.vpc_id)

        route_table = vpc.create_route_table()
        self.route_table_id = route_table.id

        if self.is_public:
            route_table.create_route(DestinationCidrBlock='0.0.0.0/0', GatewayId=self.internet_gateway.internet_gateway_id)

    def cleanup(self):
        """Deletes a route table.
        """
        super().cleanup()
        
        self.ec2_client.delete_route_table(RouteTableId=self.route_table_id)

@dataclass
class Subnets(Bootstrappable):
    # Inputs
    vpc_id: str
    cidr_blocks: List[str]
    is_public: bool = True
    num_subnets: int = 1
    map_public_ip: bool = True

    # Subresources
    route_table: RouteTable = field(init=False, default=None)

    # Outputs
    subnet_ids: List[str] = field(init=False, default_factory=lambda: [])

    def __post_init__(self):
        self.route_table = RouteTable(self.vpc_id, is_public=self.is_public)

    @property
    def ec2_client(self):
        return boto3.client("ec2", region_name=self.region)

    @property
    def ec2_resource(self):
        return boto3.resource("ec2", region_name=self.region)

    def bootstrap(self):
        """Creates subnets.
        """
        super().bootstrap()

        vpc = self.ec2_resource.Vpc(self.vpc_id)
        region_azs = self.get_availability_zone_names()

        for i in range(self.num_subnets):
            subnet = vpc.create_subnet(CidrBlock=self.cidr_blocks[i], AvailabilityZone=region_azs[i % len(region_azs)])
            self.subnet_ids.append(subnet.id)

            # Make a separate call to enable MapPublicIpOnLaunch since boto3
            # does not accept it in the `create_subnet` parameter list
            if self.map_public_ip:
                self.ec2_client.modify_subnet_attribute(SubnetId=subnet.id, MapPublicIpOnLaunch={'Value': True})

            self.ec2_client.associate_route_table(RouteTableId=self.route_table.route_table_id, SubnetId=subnet.id)

    def cleanup(self):
        """Deletes the subnets.
        """
        # You must delete the subnet before you can delete any of its dependencies
        for subnet in self.subnet_ids:
            self.ec2_client.delete_subnet(SubnetId=subnet)

        super().cleanup()

    def get_availability_zone_names(self):
        zones = self.ec2_client.describe_availability_zones()
        return list(map(lambda x: x['ZoneName'], zones['AvailabilityZones']))
    
@dataclass
class SecurityGroup(Bootstrappable):
    # Inputs
    vpc_id: str
    name_prefix: str = "test"
    description: str = ""

    # Outputs
    group_id: str = field(init=False)
    arn: str = field(init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 24)
        self.description = resources.random_suffix_name("description-", 34)
    
    @property
    def ec2_client(self):
        return boto3.client("ec2", region_name=self.region)

    @property
    def ec2_resource(self):
        return boto3.resource("ec2", region_name=self.region)

    def bootstrap(self):
        """Creates security group with an auto-generated name and description.
        """
        vpc = self.ec2_resource.Vpc(self.vpc_id)
        group = vpc.create_security_group(
            Description=self.description,
            GroupName=self.name,
        )
        self.group_id = group["GroupId"]
        self.arn = "arn:aws:ec2:{region}:{accId}:security-group/{sgId}".format(region=self.region, accId=self.account_id, sgId=self.group_id)

    def cleanup(self):
        """Deletes the subnets.
        """
        # You must delete the securityGroup before you can delete any of its dependencies
        self.ec2_client.delete_security_group(
            GroudId=self.group_id,
            GroupName=self.name,
        )
        super().cleanup()

@dataclass
class VPC(Bootstrappable):
    # Inputs
    name_prefix: Union[str, None] = field(default=None)
    num_public_subnet: int = 2
    num_private_subnet: int = 0

    vpc_cidr_block: str = field(default=VPC_CIDR_BLOCK)
    public_subnet_cidr_blocks: Union[List[str], None] = field(default=None)
    private_subnet_cidr_blocks: Union[List[str], None] = field(default=None)

    # Subresources
    public_subnets: Subnets = field(init=False, default=None)
    private_subnets: Subnets = field(init=False, default=None)
    security_group: SecurityGroup = field(init=False, default=None)

    # Outputs
    name: Union[str, None] = field(default=None, init=False)
    vpc_id: str = field(init=False)

    def __post_init__(self):
        # Create CIDR blocks if none specified
        if self.public_subnet_cidr_blocks is None:
            self.public_subnet_cidr_blocks = [f"10.0.{r}.0/24" for r in range(self.num_public_subnet)]

        if self.private_subnet_cidr_blocks is None:
            self.private_subnet_cidr_blocks = [f"10.0.{r}.0/24" for r in range(self.num_public_subnet, self.num_private_subnet + self.num_public_subnet)]

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

        vpc = self.ec2_resource.Vpc(self.vpc_id)
        vpc.wait_until_available()

        if self.name_prefix is not None:
            self.name = resources.random_suffix_name(self.name_prefix, 63)
            self.ec2_client.create_tags(Resources=[self.vpc_id], Tags=[{'Key': 'Name', 'Value': self.name}])

        if self.num_private_subnet > 0:
            self.private_subnets = Subnets(self.vpc_id, self.private_subnet_cidr_blocks, is_public=False, num_subnets=self.num_private_subnet)
        if self.num_public_subnet > 0:
            self.public_subnets = Subnets(self.vpc_id, self.public_subnet_cidr_blocks, is_public=True, num_subnets=self.num_public_subnet)
        self.security_group = SecurityGroup(vpc_id=self.vpc_id)

        # Because we require the VPC to be generated before generating other
        # resources, if the subresources fail while bootstrapping, we need to
        # make sure to clean up the VPC before raising the error
        try:
            super().bootstrap()
        except BootstrapFailureException as ex:
            vpc.delete()
            raise ex

    @property
    def cleanup_retries(self):
        return 30

    @property
    def cleanup_interval_sec(self):
        return 60 # one minute

    def cleanup(self):
        """Deletes a VPC.
        """
        super().cleanup()

        vpc = self.ec2_resource.Vpc(self.vpc_id)
        vpc.delete()
