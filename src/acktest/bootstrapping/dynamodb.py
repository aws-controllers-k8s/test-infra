import boto3

from dataclasses import dataclass, field
from typing import List

from . import Bootstrappable
from .. import resources

@dataclass
class Table(Bootstrappable):
    # Inputs
    name_prefix: str
    attribute_definitions: List[dict]
    key_schema: List[dict]
    stream_specification: dict

    # Outputs
    name: str = field(init=False)
    latest_stream_arn: str = field(init=False)
    
    @property
    def dynamodb_client(self):
        return boto3.client("dynamodb", region_name=self.region)

    @property
    def dynamodb_resource(self):
        return boto3.resource("dynamodb", region_name=self.region)

    def bootstrap(self):
        """Creates a Dynamodb table with an auto-generated name.
        """
        self.name = resources.random_suffix_name(self.name_prefix, 63)
        table = self.dynamodb_client.create_table(
            TableName=self.name,
            KeySchema=self.key_schema,
            AttributeDefinitions=self.attribute_definitions,
            StreamSpecification=self.stream_specification,
        )
        self.latest_stream_arn = table.latest_stream_arn

    def cleanup(self):
        """Deletes the dynamodb table.
        """
        self.dynamodb_client.delete_table(
            TableName=self.name,
        )