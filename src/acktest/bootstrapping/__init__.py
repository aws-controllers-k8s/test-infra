from __future__ import annotations

import abc
import pickle
import logging

from pathlib import Path
from dataclasses import dataclass, fields
from typing import Iterator

from ..aws.identity import get_region

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
    region_override: str
    
    @abc.abstractmethod
    def bootstrap(self):
        pass

    @abc.abstractmethod
    def cleanup(self):
        pass

    @property
    def region(self):
        return get_region() if self.region_override is None else self.region_override

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
        logging.info("🛠️ Bootstrapping resources ...")
        for resource in self.iter_bootstrappable:
            resource.bootstrap()

    def cleanup(self):
        """Runs the `cleanup` method for every `BootstrappableResource` 
            subclass in the bootstrap dictionary.
        """
        logging.info("🧹 Cleaning up resources ...")
        for resource in self.iter_bootstrappable:
            resource.cleanup()