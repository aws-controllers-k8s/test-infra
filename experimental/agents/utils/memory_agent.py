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
"""Memory agent for ACK agents."""

from logging import getLogger
from typing import Optional

from strands import Agent
from strands_tools import mem0_memory

from config.defaults import MEM0_USER_ID

# Memory Agent System Prompt - specialized for ACK build error/solution management
MEMORY_AGENT_SYSTEM_PROMPT = """You are the ACK Memory Agent, a specialized assistant that manages error-solution knowledge for AWS Controllers for Kubernetes (ACK) code generation and build processes.

Your primary responsibilities:
1. **Store Error Solutions**: When given an error and solution, store them in memory with appropriate metadata
2. **Retrieve Solutions**: When asked about an error, search for and return relevant solutions from memory
3. **Manage Knowledge Base**: Maintain a searchable repository of ACK-specific build errors and their fixes

Key Areas of Expertise:
- ACK code generator errors and fixes
- generator.yaml configuration issues
- AWS SDK Go v2 API mapping problems
- Resource field mapping errors
- Build process failures
- Controller generation issues

When storing memories:
- Use descriptive metadata including error type, service, resource, and solution category
- Tag with relevant keywords for easy retrieval
- Include context about when and why the solution works

When retrieving memories:
- Search for semantically similar errors, not just exact matches
- Return the most relevant and recent solutions
- Provide context about when the solution was effective

Always use user_id="ack_codegen_agent_user" for all memory operations to maintain consistency across the ACK generation workflow.

Focus on being helpful, accurate, and building a comprehensive knowledge base that improves ACK controller generation over time."""


class MemoryAgent:
    """Dedicated memory agent for ACK build error/solution management."""

    def __init__(self):
        self.logger = getLogger(__name__)
        self.user_id = MEM0_USER_ID
        self.agent = Agent(system_prompt=MEMORY_AGENT_SYSTEM_PROMPT, tools=[mem0_memory])

    def store_error_solution(self, error_message: str, solution: str, metadata: dict) -> str:
        """Store an error and its solution in memory."""
        try:
            if metadata is None:
                metadata = {}
            metadata.update(
                {
                    "type": "error_solution",
                    "error_message": error_message[:200],  # Truncate for metadata
                }
            )

            # Format the content to include both error and solution
            content = f"ERROR: {error_message}\n\nSOLUTION: {solution}"

            # Use the agent to store the memory
            prompt = f"Store this error-solution pair in memory using action='store', user_id='{self.user_id}', content='{content}', metadata={metadata}"
            response = self.agent(prompt)

            self.logger.info(f"Stored error solution in memory: {error_message[:100]}...")
            return f"Successfully saved solution for error: {error_message[:100]}{'...' if len(error_message) > 100 else ''}"
        except Exception as e:
            self.logger.error(f"Failed to store error solution: {e}")
            return f"Error storing solution: {str(e)}"

    def lookup_error_solution(self, error_message: str) -> Optional[str]:
        """Look up a solution for an error message."""
        try:
            # Use the agent to search for memories
            prompt = f"Search for solutions to this error using action='retrieve', user_id='{self.user_id}', query='{error_message}', min_score=0.7, limit=3"
            response = self.agent(prompt)

            response_str = str(response)
            if "no memories" in response_str.lower() or "not found" in response_str.lower():
                self.logger.info(f"No solution found for error: {error_message[:100]}...")
                return None
            else:
                self.logger.info(f"Found solution for error: {error_message[:100]}...")
                return response_str
        except Exception as e:
            self.logger.error(f"Failed to lookup error solution: {e}")
            return f"Error looking up solution: {str(e)}"

    def search_memories(self, query: str, limit: int = 5) -> str:
        """Search through stored memories."""
        try:
            prompt = f"Search for memories related to '{query}' using action='retrieve', user_id='{self.user_id}', min_score=0.5, limit={limit}"
            response = self.agent(prompt)
            return str(response)
        except Exception as e:
            self.logger.error(f"Failed to search memories: {e}")
            return f"Error searching memories: {str(e)}"

    def add_knowledge(self, content: str, metadata: dict) -> str:
        """Add general knowledge to memory."""
        try:
            if metadata is None:
                metadata = {"type": "knowledge"}

            prompt = f"Store this knowledge in memory using action='store', user_id='{self.user_id}', content='{content}', metadata={metadata}"
            response = self.agent(prompt)

            return f"Successfully added knowledge: {content[:100]}{'...' if len(content) > 100 else ''}"
        except Exception as e:
            self.logger.error(f"Failed to add knowledge: {e}")
            return f"Error adding knowledge: {str(e)}"

    def list_all_memories(self) -> str:
        """List all stored memories."""
        try:
            prompt = f"List all memories using action='list', user_id='{self.user_id}'"
            response = self.agent(prompt)
            return str(response)
        except Exception as e:
            self.logger.error(f"Failed to list memories: {e}")
            return f"Error listing memories: {str(e)}"
