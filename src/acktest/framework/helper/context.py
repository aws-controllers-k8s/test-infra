from typing import List

import acktest.framework.helper.helper as hlp
from acktest.framework.helper.helper import FrameworkHelper

class HelperRegistrationContext:
    """Context for collecting helpers registered using their decorators."""
    def __enter__(self) -> List[FrameworkHelper]:
        registered_helpers: List[FrameworkHelper] = []

        def add_helper(cls):
            registered_helpers.append(cls)
            return cls
        
        # Backup previous handler
        self.old_handler = hlp._register_helper_handler
        hlp._register_helper_handler = add_helper
        return registered_helpers

    def __exit__(self, *args):
        hlp._register_helper_handler = self.old_handler