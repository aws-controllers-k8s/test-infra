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

from abc import abstractmethod, ABC
from dataclasses import dataclass, asdict
from typing import Dict, Tuple

from .k8s import resource as k8s
from .k8s import condition
from .resources import random_suffix_name

ADOPTED_RESOURCE_GROUP = "services.k8s.aws"
ADOPTED_RESOURCE_VERSION = "v1alpha1"
ADOPTED_RESOURCE_PLURAL = "adoptedresources"
ADOPTED_RESOURCE_KIND = "AdoptedResource"

ADOPTED_CONDITION_NAME = "ACK.Adopted"

ADOPT_ADOPTION_POLICY = "adopt"
ADOPT_OR_CREATE_ADOPTION_POLICY = "adopt-or-create"

@dataclass(frozen=True)
class AdoptedResourceAWSIdentifier:
    """Represents the base AWS identifier spec fields from the adopted resource CRD.

    Additional keys need to be configured as allowlisted as part of the 
    controller generator.
    """
    additionalKeys: Dict[str, str]

@dataclass(frozen=True)
class AdoptedResourceNameOrIDIdentifier(AdoptedResourceAWSIdentifier):
    nameOrID: str

@dataclass(frozen=True)
class AdoptedResourceARNIdentifier(AdoptedResourceAWSIdentifier):
    arn: str

@dataclass(frozen=True)
class AdoptedResourceKubernetesIdentifiers:
    """Represents the Kubernetes spec fields from the adopted resource CRD.

    This class purposefully does not expose metadata configuration elements, as
    those are configured by the test fixture.
    """
    group: str
    kind: str

@dataclass(frozen=True)
class AdoptedResourceSpec:
    """Represents the adopted resource CRD spec fields.
    """
    aws: AdoptedResourceAWSIdentifier
    kubernetes: AdoptedResourceKubernetesIdentifiers

class AbstractAdoptionTest(ABC):
    """Acts as the base class for an adoption smoke test.

    This class should be derived inside a service's end-to-end tests, overriding
    the static class variables and any of the public methods as necessary. The
    derived class *must* override the `get_resource_spec` class.

    Example (S3 bucket adoption):
    ```
    class TestAdoptBucket(adoption.AbstractAdoptionTest):
        RESOURCE_PLURAL: str = RESOURCE_PLURAL
        RESOURCE_VERSION: str = CRD_VERSION

        _bucket_name: str = random_suffix_name("ack-adopted-bucket", 63)

        def bootstrap_resource(self):
            # Create the S3 bucket using boto3
            return

        def cleanup_resource(self):
            # Delete the S3 bucket using boto3
            return

        def get_resource_spec(self) -> adoption.AdoptedResourceSpec:
            return adoption.AdoptedResourceSpec(
                aws=adoption.AdoptedResourceNameOrIDIdentifier(name_or_id=self._bucket_name),
                kubernetes=adoption.AdoptedResourceKubernetesIdentifiers(CRD_GROUP, RESOURCE_KIND),
            )

    ```

    You can define additional tests by create any new method starting with 
    `test_` in the name, just like with any other PyTest method.
    """
    RESOURCE_PLURAL: str = ""
    RESOURCE_VERSION: str = ""
    TARGET_NAMESPACE: str = "default"

    _spec: AdoptedResourceSpec  = None
    _reference: k8s.CustomResourceReference

    @abstractmethod
    def bootstrap_resource(self):
        pass

    @abstractmethod
    def cleanup_resource(self):
        pass

    @abstractmethod
    def get_resource_spec(self) -> AdoptedResourceSpec:
        pass

    def _generate_resource_name(self) -> str:
        return (random_suffix_name(f"adopted-{self._spec.kubernetes.kind}", 32, delimiter="-")).lower()

    def _create_adopted_resource(self, resource_name: str) -> Tuple[k8s.CustomResourceReference, Dict]:
        spec_dict = asdict(self._spec)
        body_dict = {
            "apiVersion": f"{ADOPTED_RESOURCE_GROUP}/{ADOPTED_RESOURCE_VERSION}",
            "kind": ADOPTED_RESOURCE_KIND,
            "metadata": {
                "name": resource_name,
                "namespace": self.TARGET_NAMESPACE
            },
            "spec": spec_dict
        }

        reference = k8s.CustomResourceReference(ADOPTED_RESOURCE_GROUP, ADOPTED_RESOURCE_VERSION,
            ADOPTED_RESOURCE_PLURAL, resource_name, namespace=self.TARGET_NAMESPACE)

        return (reference, k8s.create_custom_resource(reference, body_dict))
        

    def _assert_adoption_status(self):
        cr = k8s.wait_resource_consumed_by_controller(self._reference)

        assert cr is not None
        assert k8s.get_resource_exists(self._reference)
        condition.assert_type_status(self._reference, condition.CONDITION_TYPE_ADOPTED, True)

    def _assert_target_created(self, target_name: str):
        target_reference = k8s.CustomResourceReference(self._spec.kubernetes.group,
            self.RESOURCE_VERSION, self.RESOURCE_PLURAL, target_name, namespace=self.TARGET_NAMESPACE)
        target = k8s.get_resource(target_reference)
        
        assert target is not None

        # TODO(RedbackThomson): Additional assertions about the resource?

    def _delete_adopted_resource(self):
        _, deleted = k8s.delete_custom_resource(self._reference)
        assert deleted

    def test_smoke(self):
        # Bootstrap resource (abstract)
        self.bootstrap_resource()

        # Get resource spec (abstract)
        self._spec = self.get_resource_spec()

        # Create adopted resource
        resource_name = self._generate_resource_name()
        (self._reference, resource) = self._create_adopted_resource(resource_name)

        # Check adoption status
        self._assert_adoption_status()

        # Check presence of target resource
        self._assert_target_created(resource_name)

        # Delete adopted resource
        self._delete_adopted_resource()

        # Cleanup resource (abstract)
        self.cleanup_resource()