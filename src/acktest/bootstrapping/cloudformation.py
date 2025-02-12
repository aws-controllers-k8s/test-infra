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
import json

from dataclasses import dataclass, field

from .. import resources
from . import Bootstrappable, BootstrapFailureException


@dataclass
class Stack(Bootstrappable):
    # Inputs
    name_prefix: str
    template: dict

    @property
    def cf_client(self):
        return boto3.client("cloudformation", region_name=self.region)
    
    @property
    def cf_resource(self):
        return boto3.resource("cloudformation", region_name=self.region)
    
    def bootstrap(self):
        """Create a Cloudformation stack with an auto-generated name with a provided template.
        """
        super().bootstrap()

        self.name = resources.random_suffix_name(self.name_prefix, 24)
        
        stack = self.cf_client.create_stack(
            StackName=self.name,
            TemplateBody=json.dumps(self.template)
        )
        
        waiter = self.cf_client.get_waiter('stack_create_complete')
        waiter.wait(StackName=self.name)

    def cleanup(self):
        """Deletes a Cloudformation stack and its associated resources
        """
        self.cf_client.delete_stack(
            StackName=self.name,
        )

        waiter = self.cf_client.get_waiter("stack_delete_complete")
        err = waiter.wait(StackName=self.name)
        
        if err is not None:
            raise BootstrapFailureException(err)
        
        super().cleanup()