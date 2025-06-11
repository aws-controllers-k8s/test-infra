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
"""Main entry point for the ACK Model agent CLI."""

import argparse
import logging
import warnings

from rich.console import Console
from rich.panel import Panel
from strands import Agent
from strands.models import BedrockModel

from ack_model_agent.prompt import ACK_MODEL_AGENT_SYSTEM_PROMPT
from ack_model_agent.tools import (
    save_operations_catalog,
    save_field_catalog,
    save_operation_analysis,
    save_error_catalog,
    save_resource_characteristics,
    query_knowledge_base,
)
from config.defaults import DEFAULT_MODEL_ID, DEFAULT_REGION, DEFAULT_TEMPERATURE
from utils.formatting import pretty_markdown

console = Console()


def configure_logging(debug=False):
    """Configure logging for the application.

    Args:
        debug: Whether to enable debug logging
    """
    log_level = logging.DEBUG if debug else logging.INFO
    logging.basicConfig(level=log_level, format="%(levelname)s | %(name)s | %(message)s")

    # Always enable Strands debug logs if debug is enabled
    if debug:
        logging.getLogger("strands").setLevel(logging.DEBUG)

    # Suppress deprecation warnings from botocore about datetime.utcnow()
    warnings.filterwarnings("ignore", category=DeprecationWarning, module="botocore")


def run_agent_cli():
    """Run the agent CLI with command line arguments."""
    parser = argparse.ArgumentParser(description="ACK Model Analysis Agent CLI")
    parser.add_argument("--region", type=str, default=DEFAULT_REGION, help="AWS region for Bedrock")
    parser.add_argument(
        "--model", type=str, default=DEFAULT_MODEL_ID, help="Model ID for Claude on Bedrock"
    )
    parser.add_argument(
        "--temperature",
        type=float,
        default=DEFAULT_TEMPERATURE,
        help="Temperature for model generation",
    )
    parser.add_argument("--debug", action="store_true", help="Enable debug logging")
    parser.add_argument("--max-results", type=int, default=10, help="Maximum knowledge base results per query (default: 10)")
    parser.add_argument("--min-score", type=float, default=0.5, help="Minimum relevance score threshold (default: 0.5)")

    args = parser.parse_args()
    configure_logging(args.debug)

    # Create model provider with throttling mitigation
    bedrock_model = BedrockModel(
        model_id=args.model,
        region_name=args.region,
        temperature=args.temperature,
        # Add throttling mitigation settings
        max_tokens=4000,  # Reduce max tokens to avoid limits
    )

    # Create the agent with our 4 core ACK model analysis tools
    agent = Agent(
        model=bedrock_model,
        tools=[
            save_operations_catalog,
            save_field_catalog,
            save_operation_analysis,
            save_error_catalog,
            save_resource_characteristics,
            query_knowledge_base,
        ],
        system_prompt=ACK_MODEL_AGENT_SYSTEM_PROMPT,
    )

    console.print(
        "[bold green]ACK Model Agent initialized with 4 core tools.[/bold green]\n"
    )
    console.print(
        "[dim]Available capabilities:[/dim]\n"
        "[dim]• Generate exact CRUD operations for any AWS resource[/dim]\n"
        "[dim]• Generate complete resource information (Smithy fields, shapes, etc.)[/dim]\n"
        "[dim]• Generate complete operation details (inputs, outputs, errors)[/dim]\n"
        "[dim]• Generate tagging support for resources[/dim]\n"
    )

    while True:
        user_input = console.input("\n[bold cyan]Model Agent>[/bold cyan] ").strip()

        if user_input.lower() in ["exit", "quit", "bye"]:
            console.print("[bold yellow]Goodbye![/bold yellow]")
            break

        if not user_input:
            continue

        try:
            console.print("\n[bold]Querying knowledge base...[/bold]")
            response = agent(user_input)
            # If response is markdown, render it as markdown
            if hasattr(response, "message"):
                console.print(pretty_markdown(response.message))
            else:
                console.print(pretty_markdown(response))
        except Exception as e:
            if "ThrottlingException" in str(e) or "throttl" in str(e).lower():
                console.print(Panel(
                    "[red]Rate limit exceeded. Please wait a moment and try again.\n"
                    "Try using smaller queries or reducing the numberOfResults parameter.[/red]", 
                    title="Throttling Error", 
                    style="red"
                ))
            else:
                console.print(Panel(f"[red]Error: {e}[/red]", title="Error", style="red"))


if __name__ == "__main__":
    run_agent_cli() 