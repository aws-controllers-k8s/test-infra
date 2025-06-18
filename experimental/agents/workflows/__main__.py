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
"""ACK Workflows CLI Runner."""

import argparse
import asyncio
from rich.console import Console
from rich.panel import Panel
from rich.table import Table

from workflows.ack_resource_workflow import (
    create_ack_resource_workflow,
    ResourceAdditionInput,
    ResourceAdditionOutput,
)
from utils.logging import configure_logging

console = Console()


async def run_resource_workflow(service: str, resource: str, aws_sdk_version: str = "v1.32.6") -> ResourceAdditionOutput:
    """Run the ACK resource addition workflow."""
    workflow_input = ResourceAdditionInput(
        service=service,
        resource=resource,
        aws_sdk_version=aws_sdk_version
    )
    
    workflow = create_ack_resource_workflow()
    
    console.print(f"[bold green]Starting ACK Resource Workflow[/bold green]")
    console.print(f"Service: {service}")
    console.print(f"Resource: {resource}")
    console.print(f"AWS SDK Version: {aws_sdk_version}")
    
    result = await workflow.run(workflow_input)
    
    return result


def display_workflow_result(result: ResourceAdditionOutput):
    """Display the workflow result in a formatted way."""
    if result.success:
        console.print(Panel(
            f"[bold green]Workflow Completed Successfully![/bold green]\n\n"
            f"Service: {result.service}\n"
            f"Resource: {result.resource}\n"
            f"Status: Successfully added resource to controller",
            title="Workflow Success",
            style="green"
        ))
        
        if result.config_changes:
            console.print("\n[bold]Configuration Changes:[/bold]")
            console.print(result.config_changes)
            
        if result.build_logs:
            console.print("\n[bold]Build Status:[/bold]")
            console.print(result.build_logs)
    else:
        console.print(Panel(
            f"[bold red]Workflow Failed[/bold red]\n\n"
            f"Service: {result.service}\n"
            f"Resource: {result.resource}\n"
            f"Error: {result.error_message}",
            title="Workflow Failed", 
            style="red"
        ))


async def list_available_workflows():
    """List available workflows."""
    table = Table(title="Available ACK Workflows")
    
    table.add_column("Workflow", style="cyan", no_wrap=True)
    table.add_column("Description", style="magenta")
    table.add_column("Usage", style="green")
    
    table.add_row(
        "resource-addition",
        "Add a new resource to an existing ACK service controller",
        "workflows resource-addition --service s3 --resource AccessPoint"
    )
    
    # Future workflows can be added here
    table.add_row(
        "service-bootstrap", 
        "Bootstrap a completely new ACK service controller",
        "[dim]Coming soon...[/dim]"
    )
    
    console.print(table)


def main():
    """Main CLI entry point."""
    parser = argparse.ArgumentParser(description="ACK Workflows CLI")
    subparsers = parser.add_subparsers(dest="command", help="Available commands")
    
    # List workflows command
    list_parser = subparsers.add_parser("list", help="List available workflows")
    
    # Resource addition workflow command
    resource_parser = subparsers.add_parser(
        "resource-addition", 
        help="Add a resource to an ACK service controller"
    )
    resource_parser.add_argument("--service", required=True, help="AWS service name (e.g. s3, ec2)")
    resource_parser.add_argument("--resource", required=True, help="Resource name (e.g. Bucket, Instance)")
    resource_parser.add_argument("--aws-sdk-version", default="v1.32.6", help="AWS SDK Go version")
    resource_parser.add_argument("--debug", action="store_true", help="Enable debug logging")
    
    args = parser.parse_args()
    
    if args.command is None:
        parser.print_help()
        return
        
    if hasattr(args, 'debug'):
        configure_logging(args.debug)
    
    if args.command == "list":
        asyncio.run(list_available_workflows())
    elif args.command == "resource-addition":
        result = asyncio.run(run_resource_workflow(
            service=args.service,
            resource=args.resource,
            aws_sdk_version=args.aws_sdk_version
        ))
        display_workflow_result(result)
    else:
        console.print(f"[red]Unknown command: {args.command}[/red]")
        parser.print_help()


if __name__ == "__main__":
    main() 