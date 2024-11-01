import boto3

from dataclasses import dataclass, field

from . import Bootstrappable
from .. import resources

@dataclass
class Function(Bootstrappable):
    # Inputs
    name_prefix: str
    code_uri: str
    description: str = ""
    service: str


    # Outputs
    arn: str = field(init=False)

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
            Runtime="python3.9",
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
