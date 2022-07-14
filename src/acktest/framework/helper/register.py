import os
from importlib import import_module
from pathlib import Path
from typing import List, Mapping, Type
from acktest.framework.helper.context import HelperRegistrationContext

from acktest.framework.helper.helper import FrameworkHelper


def collect_all_helpers(test_dir: Path, helper_dir_name: str = "helpers", import_relative_package: str = "e2e") -> List[FrameworkHelper]:
    with HelperRegistrationContext() as registered_helpers:
        # Walk through the helpers directory and import each file to register
        # their decorators
        helper_dir = test_dir / helper_dir_name
        for helper in _walk_helper_files(helper_dir):
            relative_path = Path(helper).relative_to(helper_dir)
            pymodule_path = relative_path\
                .as_posix()\
                .replace(".py", "")\
                .replace("/", ".")
            import_module(f'.{helper_dir_name}.{pymodule_path}', import_relative_package)
    return registered_helpers

def _walk_helper_files(helper_root_path: Path) -> List[str]:
    """ Walk through all helper files under a common path
    """
    for root, _, files in os.walk(helper_root_path):
        for file in files:
            # Skip any dunder files
            if "__" in file or "__" in root:
                continue
            yield os.path.join(root, file)

class HelperMap:
    _helpers: Mapping[str, Type[FrameworkHelper]] = []

    def __init__(self, test_package_path: Path):
        helpers = collect_all_helpers(test_package_path)
        self._helpers = {self._hash_from_helper(helper): helper for helper in helpers}

    @staticmethod
    def _hash(resource_name: str, api_version: str):
        return f"{resource_name.lower()}/{api_version.lower()}"

    @staticmethod
    def _hash_from_helper(helper: FrameworkHelper):
        return HelperMap._hash(helper.resource_name, helper.api_version)
    
    def get(self, resource_name: str, api_version: str):
        return self._helpers[self._hash(resource_name, api_version)]

    def has(self, resource_name: str, api_version: str):
        return self._hash(resource_name, api_version) in self._helpers