import boto3

from dataclasses import dataclass, field

from . import Bootstrappable
from .. import resources
from .iam import Role
from .iam import UserPolicies

@dataclass
class Function(Bootstrappable):
    # Inputs
    name_prefix: str
    code_uri: str
    service: str
    description: str = ""

    # Subresources
    role: Role = field(init=False, default=None)

    # Outputs
    arn: str = field(init=False)

    def __post_init__(self):
        self.role = Role(
            name_prefix=self.name_prefix,
            principal_service="lambda.amazonaws.com",
            managed_policies=['arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole'],
        )

    @property
    def lambda_client(self):
        return boto3.client("lambda", region_name=self.region)

    def bootstrap(self):
        """Creates a Lambda Function with an auto-generated name.
        """
        super().bootstrap()
        self.name = resources.random_suffix_name(self.name_prefix, 63)

        function = self.lambda_client.create_function(
            FunctionName=self.name,
            Runtime="python3.8",
            Role=self.role.arn,
            Handler="index.handler",
            Code={
                "ImageURI": self.code_uri
            },
            Description=self.description,
        )

        self.arn = function["FunctionArn"]

        self.lambda_client.add_permission(
            FunctionName=self.name,
            StatementId=f"{self.name}-invoke",
            SourceArn=f"arn:aws:{self.service}:{self.region}:{resources.get_account_id()}:*",
            Action="lambda:InvokeFunction",
            Principal="elasticloadbalancer.amazonaws.com"
        )

    def cleanup(self):
        """Deletes a Lambda Function.
        """
        if self.arn:
            self.lambda_client.delete_function(FunctionName=self.name)

        super().cleanup()
