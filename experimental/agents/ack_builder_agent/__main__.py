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
"""Main entry point for the ACK agent CLI."""

import argparse

from rich.console import Console
from rich.panel import Panel

from ack_builder_agent.prompts import ACK_BUILDER_SYSTEM_PROMPT
from ack_builder_agent.tools import (
    build_controller,
    read_build_log,
    sleep,
    verify_build_completion,
)
from config.defaults import (
    DEFAULT_MODEL_ID, 
    DEFAULT_REGION, 
    DEFAULT_TEMPERATURE,
    DEFAULT_MAX_RETRY_ATTEMPTS
)
from utils.bedrock import create_enhanced_agent
from utils.formatting import pretty_markdown
from utils.logging import configure_logging

console = Console()


def run_agent_cli():
    """Run the agent CLI with command line arguments."""
    parser = argparse.ArgumentParser(description="ACK Controller Assistant CLI")
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

    args = parser.parse_args()
    configure_logging(args.debug)

    # Create the agent with enhanced reliability settings using utility function
    agent = create_enhanced_agent(
        tools=[
            build_controller,
            read_build_log,
            sleep,
            verify_build_completion,
        ],
        system_prompt=ACK_BUILDER_SYSTEM_PROMPT,
        model_id=args.model,
        region_name=args.region,
        temperature=args.temperature,
    )

    console.print(
        "[bold green]ACK Controller Builder Agent initialized. Type 'exit' to quit.[/bold green]\n"
    )

    while True:
        user_input = console.input("\n[bold cyan]Builder Agent>[/bold cyan] ").strip()

        if user_input.lower() in ["exit", "quit", "bye"]:
            console.print("[bold yellow]Goodbye![/bold yellow]")
            break

        if not user_input:
            continue

        try:
            console.print("\n[bold]Processing...[/bold]")
            response = agent(user_input)
            # If response is markdown, render it as markdown
            if hasattr(response, "message"):
                console.print(pretty_markdown(response.message))
            else:
                console.print(pretty_markdown(response))
        except Exception as e:
            if "ThrottlingException" in str(e) or "throttl" in str(e).lower():
                console.print(Panel(
                    "[red]Rate limit exceeded. The agent will automatically retry with backoff.\n"
                    f"Configured for up to {DEFAULT_MAX_RETRY_ATTEMPTS} retry attempts.[/red]", 
                    title="Throttling - Auto Retry Enabled", 
                    style="red"
                ))
            else:
                console.print(Panel(f"[red]Error: {e}[/red]", title="Error", style="red"))


if __name__ == "__main__":
    run_agent_cli()
