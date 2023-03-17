from __future__ import annotations

import abc
import pickle
import logging

from pathlib import Path
from dataclasses import dataclass, fields, asdict
from typing import Iterable, Iterator

from ..aws.identity import get_region, get_account_id

BOOTSTRAP_RETRIES = 3
CLEANUP_RETRIES = 3

class Serializable:
    """Represents a list of all bootstrappable resources required for a given
    service's tests.
    """
    def serialize(self, output_path: Path, bootstrap_file_name: str = "bootstrap.pkl"):
        """ Dumps the service bootstrap into a pickle file at a given path.

        Args:
            output_path: The directory in which to dump the bootstrap pickle.
            bootstrap_file_name: The name of the created bootstrap pickle file.
        """
        path =  output_path / bootstrap_file_name
        with open(path, "wb") as stream:
            pickle.dump(self, stream)
        logging.info(f"Wrote bootstrap to {path}")

    @classmethod
    def deserialize(cls, config_dir: Path, bootstrap_file_name: str = "bootstrap.pkl") -> Resources:
        """ Reads a service bootstrap from a given bootstrap pickle file.

        Args:
            config_dir: The directory in which the bootstrap pickle exists.
            bootstrap_file_name: The name of the created bootstrap pickle file.

        Returns:
            ServiceBootstrapResources: The servicebootstrap resources read from
                the file.
        """
        path = config_dir / bootstrap_file_name
        with open(path, "rb") as stream:
            bootstrap = pickle.load(stream)
        return bootstrap

class BootstrapFailureException(Exception):
    pass

@dataclass
class Bootstrappable(abc.ABC):
    """Represents a single bootstrappable resource.
    """

    @property
    def region(self):
        return get_region()

    @property
    def account_id(self):
        return str(get_account_id())

    @abc.abstractmethod
    def bootstrap(self):
        self._bootstrap_subresources()

    @abc.abstractmethod
    def cleanup(self):
        self._cleanup_subresources()

    @property
    def iter_bootstrappable(self) -> Iterator[Bootstrappable]:
        """Iterates over the values of each field that extends the
            `BootstrappableResource` type

        Yields:
            Iterator[BootstrappableResource]: A field value.
        """
        for field in fields(self):
            if not isinstance(field.type, type) or not issubclass(field.type, Bootstrappable):
                continue

            attr = getattr(self, field.name)
            if attr is None:
                continue

            yield attr

    def _bootstrap_subresources(self):
        """Iterates through every `Bootstrappable` field and attempts to
            bootstrap it for a given number of retries.

        If the bootstrapping fails, it will attempt to cleanup the previous
        attempt's subresources and try again. After reaching the maximum number
        of retries, it will clean up any resources that were successfully
        bootstrapped and then fail with a `BootstrapFailureException`.

        Raises:
            BootstrapFailureException: If bootstrapping attempts reached the
                maximum number of retries.
        """
        bootstrapped = []
        for resource in self.iter_bootstrappable:
            should_cleanup = True

            resource_name = type(resource).__name__
            logging.info(f"Attempting bootstrap {resource_name}")
            for _ in range(BOOTSTRAP_RETRIES):
                try:
                    # Bootstrap and add to list of successes
                    resource.bootstrap()
                    logging.info(f"Successfully bootstrapped {resource_name}")
                    bootstrapped.append(resource)
                    should_cleanup = False
                    break
                except BootstrapFailureException as ex:
                    # Don't attempt to retry if we reached maximum retries beneath
                    raise ex
                except Exception as ex:
                    logging.error(f"Exception while bootstrapping {resource_name}")
                    logging.exception(ex)
                    # Clean up any dependencies the first attempt made
                    logging.info(f"Cleaning up dependencies created by {resource_name}")
                    resource.cleanup()
                    logging.info(f"Retrying bootstrapping {resource_name}")
                    continue
            else:
                logging.error(f"🚫 Exceeded maximum retries ({BOOTSTRAP_RETRIES}) for bootstrapping {resource_name}")

            if should_cleanup:
                # Attempt to clean up successfully bootstrapped elements
                self._cleanup_resources(bootstrapped)
                raise BootstrapFailureException(f"Bootstrapping failed for resource type '{resource_name}'")

    def _cleanup_subresources(self):
        self._cleanup_resources(self.iter_bootstrappable)

    def _cleanup_resources(self, resources: Iterable[Bootstrappable]):
        """Iterates through the given list of resources and attempts to clean
            them up for a given number of retries.

        Args:
            resources (Iterable[Bootstrappable]): The resources to attempt to
                clean up.
        """
        # Iterate through list in reverse order, so that resources created last
        # (with the most dependencies) are the first to be deleted
        for resource in reversed(list(resources)):
            resource_name = type(resource).__name__
            for _ in range(CLEANUP_RETRIES):
                try:
                    # Clean up and add to list of successes
                    logging.info(f"Attempting cleanup {resource_name}")
                    resource.cleanup()
                    logging.info(f"Successfully cleaned up {resource_name}")
                    break
                except Exception as ex:
                    logging.error(f"Exception while cleaning up {resource_name}")
                    logging.exception(ex)
                    continue
            else:
                # Hit retry limit
                logging.error(f"🚫 Exceeded maximum retries ({BOOTSTRAP_RETRIES}) for cleaning up {resource_name}")
                logging.error(f"Possibly dangling resource ({resource_name}): {asdict(resource)}")

@dataclass
class Resources(Serializable, Bootstrappable):
    def bootstrap(self):
        """Runs the `bootstrap` method for every `BootstrappableResource`
            subclass in the bootstrap dictionary.
        """
        logging.info("🛠️ Bootstrapping resources ...")
        self._bootstrap_subresources()

    def cleanup(self):
        """Runs the `cleanup` method for every `BootstrappableResource`
            subclass in the bootstrap dictionary.
        """
        logging.info("🧹 Cleaning up resources ...")
        self._cleanup_subresources()
