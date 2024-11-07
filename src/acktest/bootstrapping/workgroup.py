import boto3

from dataclasses import dataclass, field

from . import Bootstrappable


@dataclass
class WorkGroup(Bootstrappable):
    # Inputs
    name_prefix: str = field(init=False)
    
    # Outputs
    name: str = field(init=False)

    @property
    def athena_client(self):
        return boto3.client("athena", region_name=self.region)

    def bootstrap(self):
        """Creates a workgroup."""
        self.name = resources.random_suffix_name(self.name_prefix, 63)
        workgroup = self.athena_client.create_work_group(Name=self.name)

    def cleanup(self):
        """Deletes a workgroup."""
        self.athena_client.delete_work_group(WorkGroup=self.name)
