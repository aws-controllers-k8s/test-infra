from __future__ import annotations

import abc
import pickle
import logging

from pathlib import Path
from dataclasses import dataclass, fields
from typing import Iterable, Iterator

from ..aws.identity import get_region

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
    def deseralize(cls, config_dir: Path, bootstrap_file_name: str = "bootstrap.pkl") -> Resources:
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

@dataclass
class Bootstrappable(abc.ABC):
    """Represents a single bootstrappable resource.
    """
   
    @abc.abstractmethod
    def bootstrap(self):
        pass

    @abc.abstractmethod
    def cleanup(self):
        pass

    @property
    def region(self):
        return get_region()

class BootstrapFailureException(Exception):
    pass

@dataclass
class Resources(Serializable, Bootstrappable):
    @property
    def iter_bootstrappable(self) -> Iterator[Bootstrappable]:
        """Iterates over the values of each field that extends the 
            `BootstrappableResource` type

        Yields:
            Iterator[BootstrappableResource]: A field value.
        """
        for field in fields(self):
            if not issubclass(field.type, Bootstrappable):
                continue

            yield getattr(self, field.name)

    def bootstrap(self):
        """Runs the `bootstrap` method for every `BootstrappableResource` 
            subclass in the bootstrap dictionary.
        """

        bootstrapped = []
        logging.info("🛠️ Bootstrapping resources ...")
        for resource in self.iter_bootstrappable:
            exceptions = []
            for _ in range(BOOTSTRAP_RETRIES):
                try:
                    # Bootstrap and add to list of successes
                    resource.bootstrap()
                    bootstrapped.append(resource)
                    break
                except Exception as ex:
                    exceptions.append(ex)
                    continue
            else:
                # Hit retry limit
                logging.error(f"🚫 Exceeded maximum retries ({BOOTSTRAP_RETRIES}) for bootstrapping resource")
                for ex in exceptions:
                    logging.exception(ex)
                # Attempt to clean up successfully bootstrapped elements
                self._cleanup_resources(bootstrapped)
                raise BootstrapFailureException()
                

    def cleanup(self):
        """Runs the `cleanup` method for every `BootstrappableResource` 
            subclass in the bootstrap dictionary.
        """
        logging.info("🧹 Cleaning up resources ...")
        self._cleanup_resources(self.iter_bootstrappable)

    def _cleanup_resources(self, resources: Iterable[Bootstrappable]):
        # Iterate through list in reverse order, so that resources created last
        # (with the most dependencies) are the first to be deleted
        for resource in reversed(list(resources)):
            exceptions = []
            for _ in range(CLEANUP_RETRIES):
                try:
                    # Clean up and add to list of successes
                    resource.cleanup()
                    break
                except Exception as ex:
                    exceptions.append(ex)
                    continue
            else:
                # Hit retry limit
                logging.error(f"🚫 Exceeded maximum retries ({BOOTSTRAP_RETRIES}) for cleaning up resource")
                for ex in exceptions:
                    logging.exception(ex)
