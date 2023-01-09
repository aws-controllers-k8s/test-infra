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
"""Helper methods and structures for cleanly interacting with the K8s
CustomResource APIs.
"""

import logging
import base64
import os
import distutils.util as util
from pathlib import Path
from time import sleep
from typing import Dict, Optional, Union
from dataclasses import dataclass
from kubernetes import config, client
from kubernetes.client.api_client import ApiClient
from kubernetes.client.rest import ApiException

from ..resources import load_resource_file


@dataclass
class CustomResourceReference:
    """Stores a reference to a CustomResource within the cluster.

    Rather than repeating each field of the custom resource reference each time,
    we can pass around a single class with all the fields specified.
    """

    group: str
    version: str
    plural: str
    name: str
    namespace: Optional[str] = None

    def __str__(self):
        return self.to_short_resource_string()

    @property
    def _printable_namespace(self):
        return self.namespace or "cluster"

    def to_short_resource_string(self):
        return f"{self._printable_namespace}:{self.name}"

    def to_long_resource_string(self):
        return f"{self.plural}.{self.version}.{self.group}/{self._printable_namespace}:{self.name}"

def create_reference(crd_group: str,
                     crd_version: str,
                     resource_plural: str,
                     resource_name: str,
                     namespace: str):
    """
    Create an instance of CustomResourceReference based on the parameters

    :param crd_group: CRD Group
    :param crd_version: CRD version
    :param resource_plural: resource plural
    :param resource_name: name of resource to be created in cluster
    :param namespace: namespace in which resource should be created

    :return: an instance of CustomResourceReference
    """
    reference = CustomResourceReference(
        crd_group, crd_version, resource_plural, resource_name, namespace=namespace
    )
    return reference

def create_resource(reference: CustomResourceReference,
                    spec: object, 
                    wait_periods: int, 
                    period_length: int):
    """
    Create a resource from the reference and wait to be consumed by controller
    
    :param reference: instance of CustomResourceReference which needs to be created
    :param spec: spec of the resource corresponding to the reference

    :return: resource if it was created successfully, otherwise None
    """
    resource = create_custom_resource(reference, spec)
    resource = wait_resource_consumed_by_controller(reference, wait_periods, period_length)
    return resource

def load_and_create_resource(resource_directory: Path,
                             crd_group: str,
                             crd_version: str,
                             resource_plural: str,
                             resource_name: str,
                             spec_file_name: str,
                             replacements: object,
                             namespace: str = "default",
                             wait_periods: int = 3,
                             period_length: int = 10):
    """
    Helper method to encapsulate the common methods used to create a resource.
    Load a spec file from disk, create an instance of CustomResourceReference and resource in K8s cluster.
    See respective methods for parameter definitions and return types

    :returns: an instance of CustomResourceReference, spec loaded from disk, resource created from the reference
    """
    spec = load_resource_file(resource_directory, spec_file_name, replacements)
    reference = create_reference(crd_group, crd_version, resource_plural, resource_name, namespace)
    resource = create_resource(reference, spec, wait_periods, period_length)
    return reference, spec, resource


def _get_k8s_api_client() -> ApiClient:
    # Create new client everytime to avoid token refresh issues
    # https://github.com/kubernetes-client/python/issues/741
    # https://github.com/kubernetes-client/python-base/issues/125
    if bool(util.strtobool(os.environ.get('LOAD_IN_CLUSTER_KUBECONFIG', 'false'))):
        config.load_incluster_config()
        return ApiClient()
    return config.new_client_from_config()


def create_k8s_namespace(namespace_name: str, annotations: dict = {}):
    _api_client = _get_k8s_api_client()

    namespace = client.V1Namespace(metadata=client.V1ObjectMeta(
        name=namespace_name.lower(),annotations=annotations))

    return client.CoreV1Api(_api_client).create_namespace(namespace)


def delete_k8s_namespace(namespace_name: str):
    _api_client = _get_k8s_api_client()
    return client.CoreV1Api(_api_client).delete_namespace(namespace_name.lower())


def create_custom_resource(
        reference: CustomResourceReference, custom_resource: dict):
    _api_client = _get_k8s_api_client()
    _api = client.CustomObjectsApi(_api_client)

    if reference.namespace is None:
        return _api.create_cluster_custom_object(
            reference.group.lower(),
            reference.version.lower(),
            reference.plural.lower(),
            custom_resource
        )
    return _api.create_namespaced_custom_object(
        reference.group.lower(),
        reference.version.lower(),
        reference.namespace.lower(),
        reference.plural.lower(),
        custom_resource
    )

def patch_custom_resource(
    reference: CustomResourceReference, custom_resource: dict):
    _api_client = _get_k8s_api_client()
    _api = client.CustomObjectsApi(_api_client)

    if reference.namespace is None:
        return _api.patch_cluster_custom_object(
            reference.group.lower(),
            reference.version.lower(),
            reference.plural.lower(),
            reference.name.lower(),
            custom_resource
        )
    return _api.patch_namespaced_custom_object(
        reference.group.lower(),
        reference.version.lower(),
        reference.namespace.lower(),
        reference.plural.lower(),
        reference.name.lower(),
        custom_resource
    )

def replace_custom_resource(
    reference: CustomResourceReference, custom_resource: dict):
    _api_client = _get_k8s_api_client()
    _api = client.CustomObjectsApi(_api_client)

    if reference.namespace is None:
        return _api.replace_cluster_custom_object(
            reference.group, reference.version, reference.plural, reference.name, custom_resource)
    return _api.replace_namespaced_custom_object(
        reference.group, reference.version, reference.namespace, reference.plural, reference.name, custom_resource)

def delete_custom_resource(
    reference: CustomResourceReference, wait_periods: int = 1, period_length: int = 5):
    """Delete custom resource from cluster and wait for it to be removed by the server
    for wait_periods * period_length seconds.

    Returns:
        response, bool:
        response is APIserver response for the operation.
        bool is true if resource was removed from the server and false otherwise
    """
    _api_client = _get_k8s_api_client()
    _api = client.CustomObjectsApi(_api_client)

    _response = None
    if reference.namespace is None:
        _response = _api.delete_cluster_custom_object(
            reference.group.lower(),
            reference.version.lower(),
            reference.plural.lower(),
            reference.name.lower()
        )
    _response = _api.delete_namespaced_custom_object(
        reference.group.lower(),
        reference.version.lower(),
        reference.namespace.lower(),
        reference.plural.lower(),
        reference.name.lower()
    )

    for _ in range(wait_periods):
        sleep(period_length)
        if not get_resource_exists(reference):
            return _response, True

    logging.error(
        f"Wait for resource {reference} to be removed by server timed out")
    return _response, False


def get_resource(reference: CustomResourceReference):
    """Get the resource from a given reference.

    Returns:
        None or object: None if the resource doesnt exist in server, otherwise the
            custom object.
    """
    _api_client = _get_k8s_api_client()
    _api = client.CustomObjectsApi(_api_client)

    if reference.namespace is None:
        return _api.get_cluster_custom_object(
            reference.group.lower(),
            reference.version.lower(),
            reference.plural.lower(),
            reference.name.lower()
        )

    return _api.get_namespaced_custom_object(
        reference.group.lower(),
        reference.version.lower(),
        reference.namespace.lower(),
        reference.plural.lower(),
        reference.name.lower()
    )


def get_resource_exists(reference: CustomResourceReference) -> bool:
    try:
        return get_resource(reference) is not None
    except ApiException:
        return False


def wait_resource_consumed_by_controller(
        reference: CustomResourceReference, wait_periods: int = 3, period_length: int = 10):
    if not get_resource_exists(reference):
        logging.error(f"Resource {reference} does not exist")
        return None

    for _ in range(wait_periods):
        resource = get_resource(reference)

        if 'status' in resource:
            return resource

        sleep(period_length)

    logging.error(
        f"Wait for resource {reference} to be consumed by controller timed out")
    return None


def get_resource_arn(resource: object) -> Union[None, str]:
    """Get the .status.ackResourceMetadata.arn value from a given resource.

    Returns:
        None or string: None if the status field doesn't exist, otherwise the
            field value.
    """
    if 'ackResourceMetadata' in resource['status'] and \
        'arn' in resource['status']['ackResourceMetadata']:
        return resource['status']['ackResourceMetadata']['arn']
    return None


def create_opaque_secret(namespace: str,
                         name: str,
                         key: str,
                         value: str):
    """
    Create new k8 Opaque Secret.

    :param namespace: Namespace of the secret.
    :param name: Name of the secret
    :param key: Key of the secret
    :param value: Value of the secret
    :return: None
    """
    _api_client = _get_k8s_api_client()
    body = client.V1Secret()
    body.api_version = 'v1'
    body.data = {key:base64.b64encode(value.encode('ascii')).decode('utf-8')}
    body.kind = 'Secret'
    body.metadata = {'name': name}
    body.type = 'Opaque'
    body = _api_client.sanitize_for_serialization(body)
    client.CoreV1Api(_api_client).create_namespaced_secret(namespace.lower(),body)


def delete_secret(namespace: str,
                  name: str):
    """
    Delete an existing k8 secret.

    :param namespace: Namespace of the secret.
    :param name: Name of the secret
    :return: None
    """
    _api_client = _get_k8s_api_client()
    client.CoreV1Api(_api_client).delete_namespaced_secret(name.lower(), namespace.lower())

def wait_on_condition(reference: CustomResourceReference,
                      condition_name: str,
                      desired_condition_status: str,
                      wait_periods: int = 2,
                      period_length: int = 60) -> bool:
    """
    Waits for the specified condition in .status.conditions to reach the desired value.

    Precondition:
        resource must be consumed by the controller (i.e. have a .status field)

    Returns:
        False if the resource doesn't exist, have .status.conditions at all, have the requested
            condition type, or if the wait times out. True otherwise.
    """

    if not get_resource_exists(reference):
        logging.error(f"Resource {reference} does not exist")
        return False

    desired_condition = None
    for i in range(wait_periods):
        logging.debug(f"Waiting on condition {condition_name} to reach {desired_condition_status} for resource {reference} ({i})")

        desired_condition = get_resource_condition(reference, condition_name)
        if desired_condition is not None and desired_condition['status'] == desired_condition_status:
            logging.info(f"Condition {condition_name} has status {desired_condition_status}, continuing...")
            return True

        sleep(period_length)

    if not desired_condition:
        logging.error(f"Resource {reference} does not have a condition of type {condition_name}.")
    else:
        logging.error(f"Wait for condition {condition_name} to reach status {desired_condition_status} timed out. Condition has message '{desired_condition['message']}'")
    return False

def get_resource_condition(reference: CustomResourceReference, condition_name: str):
    """
    Returns the required condition from .status.conditions

    Precondition:
        resource must exist in the cluster

    Returns:
        condition json if it exists. None otherwise
    """
    if not get_resource_exists(reference):
        logging.error(f"Resource {reference} does not exist")
        return None

    resource = get_resource(reference)
    if 'status' not in resource or 'conditions' not in resource['status']:
        logging.error(f"Resource {reference} does not have a .status.conditions field.")
        return None

    for condition in resource['status']['conditions']:
        if condition['type'] == condition_name:
            return condition

    return None

def assert_condition_state_message(reference: CustomResourceReference,
                                   condition_name: str,
                                   desired_condition_status: str,
                                   desired_condition_message: Union[None, str]):
    """
    Helper method to check the state and message of a condition on resource.
    Caller can pass None for desired_condition_message if expected message is nil

    Returns:
        bool: True if condition exists and both the status and message match the desired values
    """
    condition = get_resource_condition(reference, condition_name)
    # Ensure the status existed
    if condition is None:
        logging.error(f"Resource {reference} does not have a condition of type {condition_name}")
        return False

    current_condition_status = condition.get('status', None)
    current_condition_message = condition.get('message', None)
    if current_condition_status == desired_condition_status and current_condition_message == desired_condition_message:
        logging.info(f"Condition {condition_name} has status {desired_condition_status} and message {desired_condition_message}, continuing...")
        return True

    logging.error(f"Resource {reference} has {condition_name} set {current_condition_status}, expected {desired_condition_status}; with message"
                    f" {current_condition_message}, expected {desired_condition_message}")
    return False
