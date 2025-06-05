# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may not use this file except in compliance
# with the License. A copy of the License is located at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# or in the 'license' file accompanying this file. This file is distributed on an 'AS IS' BASIS, WITHOUT WARRANTIES
# OR CONDITIONS OF ANY KIND, express or implied. See the License for the specific language governing permissions
# and limitations under the License.
"""Settings for ACK agents."""

import os
from typing import Optional

from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    """Configuration settings for the ACK tooling."""

    ack_root: str = os.path.expanduser("~/aws-controllers-k8s")
    ack_org_url: str = "https://github.com/aws-controllers-k8s"

    code_generator_url: str = f"{ack_org_url}/code-generator"
    code_generator_path_override: Optional[str] = None

    runtime_url: str = f"{ack_org_url}/runtime"
    runtime_path_override: Optional[str] = None

    aws_sdk_go_v2_url: str = "https://github.com/aws/aws-sdk-go-v2.git"
    aws_sdk_go_v2_path_override: Optional[str] = None

    build_logs_dir_override: str = os.path.join(ack_root, "build_logs")

    @property
    def code_generator_path(self) -> str:
        """Get the path to the code generator repo.

        Returns:
            Path to the code generator repo.
        """

        return self.code_generator_path_override if self.code_generator_path_override else os.path.join(self.ack_root, "code-generator")

    @property
    def runtime_path(self) -> str:
        """Get the path to the runtime repo.

        Returns:
            Path to the runtime repo.
        """

        return self.runtime_path_override if self.runtime_path_override else os.path.join(self.ack_root, "runtime")
    
    @property
    def aws_sdk_go_v2_path(self) -> str:
        """Get the path to the aws-sdk-go-v2 repo.

        Returns:
            Path to the aws-sdk-go-v2 repo.
        """

        return self.aws_sdk_go_v2_path_override if self.aws_sdk_go_v2_path_override else os.path.join(self.ack_root, "aws-sdk-go-v2")
    
    @property
    def build_logs_path(self) -> str:
        """Get the path to the build logs directory.

        Returns:
            Path to the build logs directory.
        """

        return self.build_logs_dir_override if self.build_logs_dir_override else os.path.join(self.ack_root, "build_logs")
    
    def get_controller_path(self, service: str) -> str:
        """Get the path to a service controller repository.

        Args:
            service: AWS service name (e.g., 's3', 'dynamodb')

        Returns:
            Path to the local service controller repository
        """
        return os.path.join(self.ack_root, f"{service}-controller")

    def get_aws_service_model_path(self, service: str) -> str:
        """Get the path to an AWS service model file.

        Args:
            service: AWS service name (e.g., 's3', 'dynamodb')

        Returns:
            Path to the service model JSON file
        """
        return os.path.join(
            self.aws_sdk_go_v2_path,
            "codegen",
            "sdk-codegen",
            "aws-models",
            f"{service.lower()}.json",
        )


settings = Settings()
