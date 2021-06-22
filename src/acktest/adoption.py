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
from .resources import random_suffix_name

ADOPTED_RESOURCE_GROUP = "services.k8s.aws"
ADOPTED_RESOURCE_VERSION = "v1alpha1"
ADOPTED_RESOURCE_PLURAL = "AdoptedResources"

ADOPTED_CONDITION_NAME = "ACK.Adopted"

@dataclass(frozen=True)
class AdoptedResourceAWSIdentifiers:
    """ Represents the AWS identifier spec fields from the adopted resource CRD.

    Additional keys need to be configured as allowlisted as part of the 
    controller generator.
    """
    nameOrId: str
    additionalKeys: Dict[str, str]

@dataclass(frozen=True)
class AdoptedResourceKubernetesIdentifiers:
    """ Represents the Kubernetes spec fields from the adopted resource CRD.

    This class purposefully does not expose metadata configuration elements, as
    those are configured by the test fixture.
    """
    group: str
    kind: str

@dataclass(frozen=True)
class AdoptedResourceSpec:
    """ Represents the adopted resource CRD spec fields.
    """
    aws: AdoptedResourceAWSIdentifiers
    kubernetes: AdoptedResourceKubernetesIdentifiers

class AbstractAdoptionTest(ABC):
    _spec: AdoptedResourceSpec  = None
    _plural: str = ""
    _version: str = ""
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

    @abstractmethod
    def get_resource_plural(self) -> str:
        pass

    @abstractmethod
    def get_resource_version(self) -> str:
        pass

    def _generate_resource_name(self) -> str:
        return random_suffix_name(f"adopted-{self._spec.kubernetes.kind}", 32, delimiter="-")

    def _create_adopted_resource(self, resource_name: str) -> Tuple[k8s.CustomResourceReference, Dict]:
        spec_dict = asdict(self._spec)

        reference = k8s.CustomResourceReference(ADOPTED_RESOURCE_GROUP, ADOPTED_RESOURCE_VERSION,
            ADOPTED_RESOURCE_PLURAL, resource_name)

        return (reference, k8s.create_custom_resource(reference, spec_dict))
        

    def _assert_adoption_status(self):
        cr = k8s.wait_resource_consumed_by_controller(self._reference)

        assert cr is not None
        assert k8s.get_resource_exists(self._reference)
        assert k8s.wait_on_condition(self._reference, ADOPTED_CONDITION_NAME, "True")

    def _assert_target_created(self, target_name: str):
        target_reference = k8s.CustomResourceReference(self._spec.kubernetes.group,
            self._version, self._plural, target_name)
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
        self._plural = self.get_resource_plural()
        self._version = self.get_resource_version()

        # Create adopted resource
        resource_name = self._generate_resource_name()
        (self._reference, resource) = self._create_adopted_resource(resource_name)

        # Check adoption status
        self._assert_adoption_status(resource)

        # Check presence of target resource
        self._assert_target_created(resource)

        # Delete adopted resource
        self._delete_adopted_resource(resource)

        # Cleanup resource (abstract)
        self.cleanup_resource()