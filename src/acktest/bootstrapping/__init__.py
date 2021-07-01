from __future__ import annotations

import abc
import pickle
import logging

from pathlib import Path
from dataclasses import dataclass, fields
from typing import Iterator

@dataclass
class ServiceBootstrapResources(abc.ABC):
    """Represents a list of all bootstrappable resources required for a given
    service's tests.
    """
    def serialize(self, output_path: Path, bootstrap_file_name: str = "bootstrap.pkl"):
        """ Dumps the service bootstrap into a pickle file at a given path.

        Args:
            bootstrap: The bootstrap object.
            output_path: The directory in which to dump the bootstrap pickle.
            bootstrap_file_name: The name of the created bootstrap pickle file.
        """
        path =  output_path / bootstrap_file_name
        with open(path, "wb") as stream:
            pickle.dump(self, stream)
        logging.info(f"Wrote bootstrap to {path}")

    @classmethod
    def deseralize(cls, config_dir: Path, bootstrap_file_name: str = "bootstrap.pkl") -> ServiceBootstrapResources:
        """ Reads a service bootstrap from a given bootstrap pickle file.

        Args:
            config_dir: The directory in which the bootstray yaml exists.
            bootstrap_file_name: The name of the created bootstrap yaml file.

        Returns:
            ServiceBootstrapResources: The servicebootstrap resources read from
                the file.
        """
        path = config_dir / bootstrap_file_name
        with open(path, "rb") as stream:
            bootstrap = pickle.load(stream)
        return bootstrap

    @property
    def bootstrappable_field_values(self) -> Iterator[BootstrappableResource]:
        """Iterates over the values of each field that extends the 
            `BootstrappableResource` type

        Yields:
            Iterator[BootstrappableResource]: A field value.
        """
        for field in fields(self):
            if not issubclass(field.type, BootstrappableResource):
                continue

            yield getattr(self, field.name)

    def bootstrap(self):
        """Runs the `bootstrap` method for every `BootstrappableResource` 
            subclass in the bootstrap dictionary.
        """
        logging.info("🛠️ Bootstrapping resources ...")
        for resource in self.bootstrappable_field_values:
            resource.bootstrap()

    def cleanup(self):
        """Runs the `cleanup` method for every `BootstrappableResource` 
            subclass in the bootstrap dictionary.
        """
        logging.info("🧹 Cleaning up resources ...")
        for resource in self.bootstrappable_field_values:
            resource.cleanup()

@dataclass
class BootstrappableResource(abc.ABC):
    """Represents a single bootstrappable resource.
    """
    
    @abc.abstractmethod
    def bootstrap(self):
        pass

    @abc.abstractmethod
    def cleanup(self):
        pass