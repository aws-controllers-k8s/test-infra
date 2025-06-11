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
"""Common formatting utilities for ACK agents."""

import json
import re
from rich.markdown import Markdown


def pretty_markdown(md) -> Markdown:
    """Convert various data types to pretty-formatted markdown.
    
    Args:
        md: Data to format - can be dict, str, or any other type
        
    Returns:
        Markdown: Rich markdown object for console rendering
    """
    # If it's a dict, pretty-print as JSON
    if isinstance(md, dict):
        md = json.dumps(md, indent=2)
    elif not isinstance(md, str):
        md = str(md)
    clean = re.sub(r"(\n\s*){3,}", "\n\n", (md or "").strip())
    return Markdown(clean) 