import abc
from types import FunctionType
from typing import ClassVar, Optional, Type

_register_helper_handler: Optional[FunctionType] = None

def register_helper(resource: str, plural: str, api_version: str):
    """Decorator for registering a class as a framework helper.

    Usage:
    ```python
    @register_helper(
        resource="bucket",
        plural="buckets",
        api_version="v1alpha1"
    )
    ```
    """

    def _register_helper(cls: Type):
        if not issubclass(cls, FrameworkHelper):
            raise TypeError(f'cannot register helper {cls.__name__}. Must be a subclass of {FrameworkHelper.__name__}')

        global _register_helper_handler

        # Initialize class variables
        cls.resource_name = resource
        cls.plural = plural
        cls.api_version = api_version

        if _register_helper_handler:
            return _register_helper_handler(cls) or cls
        return cls

    return _register_helper

class FrameworkHelper:
    resource_name: ClassVar[str] = ""
    plural: ClassVar[str] = ""
    api_version: ClassVar[str] = ""

    @classmethod
    def get_helper_hash(cls):
        return get_helper_hash(cls.resource_name, cls.api_version)

def get_helper_hash(resource_name: str, api_version: str):
    return f"{resource_name}/{api_version}"