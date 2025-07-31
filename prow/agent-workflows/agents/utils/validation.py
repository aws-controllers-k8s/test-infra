# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may not use this file except in compliance
# with the License. A copy of the License is located at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# or in the 'license' file accompanying this file. This file is distributed on an 'AS IS' BASIS, WITHOUT WARRANTIES
# OR CONDITIONS OF ANY KIND, express or implied. See the License for the specific language governing permissions
# and limitations under the License.

import os
from pathlib import Path


def is_path_under_directory(filepath: str, directory: str) -> bool:
    """
    Verify if a filepath is under a specified directory.
    
    Args:
        filepath: The path to check
        directory: The directory that should contain the filepath
        
    Returns:
        bool: True if the filepath is under the directory, False otherwise
    """
    # Convert both paths to absolute and resolve any symlinks
    filepath = os.path.abspath(os.path.normpath(filepath))
    directory = os.path.abspath(os.path.normpath(directory))
    
    # Use pathlib for safer path comparison
    return Path(filepath).is_relative_to(Path(directory))