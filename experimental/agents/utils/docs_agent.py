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
"""AWS Documentation Agent for ACK Generator tools using MCP server."""

from logging import getLogger

from mcp import StdioServerParameters, stdio_client
from strands import Agent
from strands.tools.mcp import MCPClient

# Docs Agent System Prompt - specialized for AWS documentation research
DOCS_AGENT_SYSTEM_PROMPT = """You are an AWS documentation assistant specialized in helping with ACK (AWS Controllers for Kubernetes) code generation.

Your primary role is to help users find and understand AWS service documentation, API references, and best practices that are relevant for generating Kubernetes controllers.

When searching or reading documentation:
1. Focus on API operations, resource configurations, and service-specific details
2. Pay attention to resource lifecycle, field mappings, and constraints
3. Identify key information for generator.yaml configuration
4. Look for tagging support, primary identifiers, and immutable fields
5. Note any special considerations or dependencies

Always provide clear, actionable information that can be used to configure ACK controllers effectively."""


class DocsAgent:
    """AWS Documentation agent using the AWS documentation MCP server."""

    def __init__(self):
        self.logger = getLogger(__name__)
        self.mcp_client = None
        self.agent = None
        self._initialize_mcp_client()

    def _initialize_mcp_client(self):
        """Initialize the MCP client for AWS documentation server."""
        try:
            # Create MCP client for AWS documentation server
            self.mcp_client = MCPClient(
                lambda: stdio_client(
                    StdioServerParameters(
                        command="uvx", args=["awslabs.aws-documentation-mcp-server@latest"]
                    )
                )
            )

            # Initialize the agent with MCP tools
            with self.mcp_client:
                mcp_tools = self.mcp_client.list_tools_sync()
                self.agent = Agent(tools=mcp_tools, system_prompt=DOCS_AGENT_SYSTEM_PROMPT)

            self.logger.info("AWS Documentation MCP client initialized successfully")

        except Exception as e:
            self.logger.error(f"Failed to initialize AWS Documentation MCP client: {e}")
            self.mcp_client = None
            self.agent = None

    def search_documentation(self, query: str, max_results: int = 5) -> str:
        """
        Search AWS documentation using the MCP server.

        Args:
            query: Search query for AWS documentation
            max_results: Maximum number of results to return

        Returns:
            str: Search results or error message
        """
        if not self.agent or not self.mcp_client:
            return "Error: AWS Documentation MCP client not available. Please check the connection."

        try:
            with self.mcp_client:
                # Use the agent to search documentation
                search_prompt = f"Search AWS documentation for: {query}. Return the top {max_results} most relevant results with URLs and brief descriptions."
                response = self.agent(search_prompt)
                return str(response)

        except Exception as e:
            self.logger.error(f"Failed to search AWS documentation: {e}")
            return f"Error searching AWS documentation: {str(e)}"

    def read_documentation_page(
        self, url: str, max_length: int = 5000, start_index: int = 0
    ) -> str:
        """
        Read a specific AWS documentation page.

        Args:
            url: AWS documentation URL
            max_length: Maximum characters to return
            start_index: Starting character index

        Returns:
            str: Documentation page content or error message
        """
        if not self.agent or not self.mcp_client:
            return "Error: AWS Documentation MCP client not available. Please check the connection."

        if not url.startswith("https://docs.aws.amazon.com"):
            return "Error: URL must be from docs.aws.amazon.com domain"

        try:
            with self.mcp_client:
                # Use the agent to read the documentation page
                read_prompt = f"Read the AWS documentation page at {url}. Extract the key information relevant to API operations, resource configurations, and controller generation. Max length: {max_length}, start index: {start_index}"
                response = self.agent(read_prompt)
                return str(response)

        except Exception as e:
            self.logger.error(f"Failed to read AWS documentation page: {e}")
            return f"Error reading AWS documentation page: {str(e)}"

    def get_documentation_recommendations(self, url: str) -> str:
        """
        Get recommendations for related AWS documentation pages.

        Args:
            url: AWS documentation URL to get recommendations for

        Returns:
            str: List of recommended pages or error message
        """
        if not self.agent or not self.mcp_client:
            return "Error: AWS Documentation MCP client not available. Please check the connection."

        if not url.startswith("https://docs.aws.amazon.com"):
            return "Error: URL must be from docs.aws.amazon.com domain"

        try:
            with self.mcp_client:
                # Use the agent to get recommendations
                rec_prompt = f"Get recommendations for related AWS documentation pages based on {url}. Focus on highly rated, new features, and related API documentation that would be useful for ACK controller development."
                response = self.agent(rec_prompt)
                return str(response)

        except Exception as e:
            self.logger.error(f"Failed to get documentation recommendations: {e}")
            return f"Error getting documentation recommendations: {str(e)}"

    def find_service_documentation(self, service: str, resource: str) -> str:
        """
        Find specific documentation for an AWS service and optionally a resource.

        Args:
            service: AWS service name (e.g., 's3', 'dynamodb')
            resource: Optional specific resource name

        Returns:
            str: Documentation search results
        """
        if resource:
            query = f"AWS {service} {resource} API reference documentation"
        else:
            query = f"AWS {service} API reference user guide"

        return self.search_documentation(query, max_results=3)
