from typing import List, Union
import boto3
import logging
import time

from botocore.exceptions import ClientError
from contextlib import contextmanager
from dataclasses import dataclass, field

from . import BootstrapFailureException, Bootstrappable
from .. import resources

# Subnets inside the default VPC CIDR block will be of form 10.0.*.0/24
VPC_CIDR_BLOCK = "10.0.0.0/16"

# EC2 error codes meaning "this resource is already gone". A cleanup that hits
# one of these has nothing left to do.
ALREADY_DELETED_ERROR_CODES = (
    "InvalidGroup.NotFound",
    "InvalidSubnetID.NotFound",
    "InvalidVpcID.NotFound",
    "InvalidRouteTableID.NotFound",
    "InvalidInternetGatewayID.NotFound",
    "InvalidTransitGatewayID.NotFound",
    "InvalidAllocationID.NotFound",
    "Gateway.NotAttached",
)

# How long to wait for network interfaces to disappear before deleting subnets.
# Test resources built in a bootstrap subnet (NAT gateways, VPC endpoints, load
# balancers) release their ENIs asynchronously, a little after the resource
# itself reports deleted.
#
# The budget is SHARED across every subnet in one cleanup, not per subnet, and
# is deliberately short. Observed teardowns fall into two regimes: the ENIs
# clear within a minute, or they never clear because something still owns them
# (typically an ACK CR that did not finish deleting before the test cluster went
# away). A short shared budget covers the first regime without adding
# minutes-per-subnet to the second, which the enclosing retry loop already
# spends up to 30 minutes on.
ENI_DRAIN_TIMEOUT_SEC = 60
ENI_DRAIN_INTERVAL_SEC = 5


def _describe_enis(enis: List[dict]) -> str:
    """Summarises network interfaces for a log line: what they are and who owns
        them, so a stuck subnet delete points at the resource holding it."""
    summaries = []
    for eni in enis:
        attachment = eni.get("Attachment") or {}
        summaries.append(
            "{id} (type={type}, status={status}, description='{desc}', "
            "requester={requester}, attached_to={attached})".format(
                id=eni.get("NetworkInterfaceId"),
                type=eni.get("InterfaceType"),
                status=eni.get("Status"),
                desc=eni.get("Description", ""),
                requester=eni.get("RequesterId", "-"),
                attached=attachment.get("InstanceId")
                or attachment.get("InstanceOwnerId")
                or "-",
            )
        )
    return "; ".join(summaries)


@contextmanager
def tolerate_already_deleted(description: str):
    """Swallows the EC2 "does not exist" errors so that a cleanup is idempotent.

    A cleanup can be retried after partially succeeding: the enclosing resource
    may fail on a later subresource, and the framework then re-runs the whole
    cleanup. Without this, the already-completed deletes raise NotFound, which
    can never succeed on any subsequent attempt, so the retry loop burns every
    attempt and reports a dangling resource that does not exist.
    """
    try:
        yield
    except ClientError as ex:
        code = ex.response.get("Error", {}).get("Code")
        if code not in ALREADY_DELETED_ERROR_CODES:
            raise
        logging.info(f"{description} already deleted ({code}), treating as cleaned up")

@dataclass
class TransitGateway(Bootstrappable):

    # Outputs
    transit_gateway_id: str = field(init=False)

    @property
    def ec2_client(self):
        return boto3.client("ec2", region_name=self.region)

    @property
    def ec2_resource(self):
        return boto3.resource("ec2", region_name=self.region)

    def bootstrap(self):
        """Creates a transit gateway.
        """
        transit_gateway = self.ec2_client.create_transit_gateway()
        self.transit_gateway_id = transit_gateway['TransitGateway']['TransitGatewayId']

    def cleanup(self):
        """Deletes a transit gateway.
        """
        super().cleanup()

        with tolerate_already_deleted(f"transit gateway {self.transit_gateway_id}"):
            self.ec2_client.delete_transit_gateway(TransitGatewayId=self.transit_gateway_id)

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

        with tolerate_already_deleted(f"internet gateway {self.internet_gateway_id} attachment"):
            vpc.detach_internet_gateway(InternetGatewayId=self.internet_gateway_id)
        with tolerate_already_deleted(f"internet gateway {self.internet_gateway_id}"):
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

        with tolerate_already_deleted(f"route table {self.route_table_id}"):
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
        #
        # One drain budget for the whole call, so a subnet whose ENIs never clear
        # cannot add its own timeout on top of every sibling's.
        deadline = time.time() + ENI_DRAIN_TIMEOUT_SEC
        for subnet in self.subnet_ids:
            # Wait for the ENIs left behind by test resources (NAT gateways, VPC
            # endpoints, load balancers) to be released. AWS frees them
            # asynchronously, so deleting immediately raises DependencyViolation
            # and fails the whole enclosing cleanup.
            self._wait_for_enis_released(subnet, deadline)
            with tolerate_already_deleted(f"subnet {subnet}"):
                self.ec2_client.delete_subnet(SubnetId=subnet)

        super().cleanup()

    def _wait_for_enis_released(self, subnet_id: str, deadline: float):
        """Blocks until no network interfaces remain in the subnet, or the shared
            drain deadline passes.

        Timing out is not an error: the caller still attempts the delete, and the
        surrounding retry loop remains the backstop. This only removes the common
        case where the subnet is a few seconds away from being deletable.

        On timeout the remaining interfaces are logged with their owner, because
        an ENI that never clears means something still holds it -- usually an ACK
        resource whose CR did not finish deleting -- and naming it is what turns
        a dangling-resource report into an actionable one.
        """
        while True:
            try:
                resp = self.ec2_client.describe_network_interfaces(
                    Filters=[{"Name": "subnet-id", "Values": [subnet_id]}],
                )
            except ClientError:
                # Subnet already gone, or we cannot see it; let the delete decide.
                return
            enis = resp.get("NetworkInterfaces", [])
            if not enis:
                return

            remaining = deadline - time.time()
            if remaining <= 0:
                logging.warning(
                    f"{len(enis)} network interface(s) still in subnet {subnet_id} "
                    f"after the {ENI_DRAIN_TIMEOUT_SEC}s drain budget; attempting "
                    f"delete anyway. Blocking interfaces: {_describe_enis(enis)}"
                )
                return
            logging.info(
                f"Waiting for {len(enis)} network interface(s) in subnet {subnet_id} "
                f"to be released before deleting it"
            )
            time.sleep(min(ENI_DRAIN_INTERVAL_SEC, remaining))

    def get_availability_zone_names(self):
        zones = self.ec2_client.describe_availability_zones()
        return list(map(lambda x: x['ZoneName'], zones['AvailabilityZones']))

@dataclass
class SecurityGroup(Bootstrappable):
    # Inputs
    vpc_id: str
    name_prefix: str = "test"
    description: str = ""
    # When True, a self-referencing inbound rule (source = this SG) is added
    # after the group is created.
    self_referencing_ingress: bool = False
    # IP protocol used when `self_referencing_ingress` is True.
    self_referencing_ingress_protocol: str = "-1"

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
        self.group_id = group.id
        self.arn = "arn:aws:ec2:{region}:{accId}:security-group/{sgId}".format(region=self.region, accId=self.account_id, sgId=self.group_id)

        if self.self_referencing_ingress:
            self._authorize_self_referencing_ingress()

    def _authorize_self_referencing_ingress(self):
        """Authorizes ingress from this security group to itself.

        Tolerates `InvalidPermission.Duplicate` so the call is idempotent if
        the rule is already present (e.g. the SG is being reused across
        bootstrap attempts).
        """
        try:
            self.ec2_client.authorize_security_group_ingress(
                GroupId=self.group_id,
                IpPermissions=[{
                    "IpProtocol": self.self_referencing_ingress_protocol,
                    "UserIdGroupPairs": [{"GroupId": self.group_id}],
                }],
            )
        except self.ec2_client.exceptions.ClientError as e:
            if "InvalidPermission.Duplicate" not in str(e):
                raise
            logging.info(
                f"Self-referencing ingress already present on "
                f"{self.group_id}; continuing"
            )

    def cleanup(self):
        """Deletes the security group.
        """
        # You must delete the securityGroup before you can delete any of its dependencies
        with tolerate_already_deleted(f"security group {self.group_id}"):
            self.ec2_client.delete_security_group(
                GroupId=self.group_id,
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
    # Propagated to the auto-created `security_group`. When True, the VPC's
    # default security group is created with a self-referencing inbound rule.
    security_group_self_referencing_ingress: bool = False

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
            self.private_subnets = Subnets(self.vpc_id, self.private_subnet_cidr_blocks, is_public=False, map_public_ip=False, num_subnets=self.num_private_subnet)
        if self.num_public_subnet > 0:
            self.public_subnets = Subnets(self.vpc_id, self.public_subnet_cidr_blocks, is_public=True, num_subnets=self.num_public_subnet)
        self.security_group = SecurityGroup(
            vpc_id=self.vpc_id,
            self_referencing_ingress=self.security_group_self_referencing_ingress,
        )

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
        with tolerate_already_deleted(f"VPC {self.vpc_id}"):
            vpc.delete()
