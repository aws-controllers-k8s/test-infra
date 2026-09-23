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
import sys
from typing import Optional
from rich.console import Console
from rich.panel import Panel
from rich.table import Table

from workflows.ack_field_workflow import (
    FieldAdditionInput,
    FieldAdditionOutput,
    create_ack_field_workflow,
)
from workflows.ack_resource_workflow import (
    create_ack_resource_workflow,
    ResourceAdditionInput,
    ResourceAdditionOutput,
)
from config.defaults import DEFAULT_MODEL_ID
from utils.logging import configure_logging

console = Console()


async def run_resource_workflow(service: str, resource: str, aws_sdk_version: Optional[str] = None, model: str = DEFAULT_MODEL_ID) -> ResourceAdditionOutput:
    """Run the ACK resource addition workflow."""
    workflow_input = ResourceAdditionInput(
        service=service,
        resource=resource,
        aws_sdk_version=aws_sdk_version,
        model_id=model,
    )
    
    workflow = create_ack_resource_workflow()
    
    console.print(f"[bold green]Starting ACK Resource Workflow[/bold green]")
    console.print(f"Service: {service}")
    console.print(f"Resource: {resource}")
    console.print(f"Model: {model}")
    if aws_sdk_version:
        console.print(f"AWS SDK Version: {aws_sdk_version}")
    
    result = await workflow.run(workflow_input)
    
    return result


async def run_field_workflow(
    service: str,
    resource: str,
    field: str,
    aws_sdk_version: Optional[str] = None,
    model: str = DEFAULT_MODEL_ID,
) -> FieldAdditionOutput:
    """Run the ACK field addition workflow."""
    workflow_input = FieldAdditionInput(
        service=service,
        resource=resource,
        field=field,
        aws_sdk_version=aws_sdk_version,
        model_id=model,
    )
    workflow = create_ack_field_workflow()

    console.print("[bold green]Starting ACK Field Workflow[/bold green]")
    console.print(f"Service: {service}")
    console.print(f"Resource: {resource}")
    console.print(f"Field: {field}")
    console.print(f"Model: {model}")
    if aws_sdk_version:
        console.print(f"AWS SDK Version: {aws_sdk_version}")

    return await workflow.run(workflow_input)


def display_workflow_result(result: ResourceAdditionOutput | FieldAdditionOutput):
    """Display the workflow result in a formatted way."""
    field_line = f"\nField: {result.field}" if isinstance(result, FieldAdditionOutput) else ""
    success_status = (
        "Successfully added field to existing resource"
        if isinstance(result, FieldAdditionOutput)
        else "Successfully added resource to controller"
    )
    if result.success:
        console.print(Panel(
            f"[bold green]Workflow Completed Successfully![/bold green]\n\n"
            f"Service: {result.service}\n"
            f"Resource: {result.resource}{field_line}\n"
            f"Status: {success_status}",
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
            f"Resource: {result.resource}{field_line}\n"
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
    table.add_row(
        "field-addition",
        "Add one field to an existing ACK resource",
        "workflows field-addition --service s3 --resource Bucket --field BucketKeyEnabled"
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
    resource_parser.add_argument("--model", default=DEFAULT_MODEL_ID, help="Bedrock model ID")
    resource_parser.add_argument("--aws-sdk-version", default=None, help="AWS SDK Go version (optional, auto-detected if not set)")
    resource_parser.add_argument("--debug", action="store_true", help="Enable debug logging")

    field_parser = subparsers.add_parser(
        "field-addition",
        help="Add a field to an existing ACK resource",
    )
    field_parser.add_argument("--service", required=True, help="AWS service name (e.g. s3, ec2)")
    field_parser.add_argument("--resource", required=True, help="Existing resource name (e.g. Bucket)")
    field_parser.add_argument("--field", required=True, help="Field name (e.g. BucketKeyEnabled)")
    field_parser.add_argument("--model", default=DEFAULT_MODEL_ID, help="Bedrock model ID")
    field_parser.add_argument(
        "--aws-sdk-version",
        default=None,
        help="AWS SDK Go version (optional, auto-detected if not set)",
    )
    field_parser.add_argument("--debug", action="store_true", help="Enable debug logging")
    
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
            aws_sdk_version=args.aws_sdk_version,
            model=args.model,
        ))
        display_workflow_result(result)
        # Exit non-zero on failure so the caller (prow-job.sh, running under
        # `set -e`) stops before committing/pushing and opening a PR for a
        # resource that did not pass review + e2e.
        if not result.success:
            sys.exit(1)
    elif args.command == "field-addition":
        result = asyncio.run(run_field_workflow(
            service=args.service,
            resource=args.resource,
            field=args.field,
            aws_sdk_version=args.aws_sdk_version,
            model=args.model,
        ))
        display_workflow_result(result)
        if not result.success:
            sys.exit(1)
    else:
        console.print(f"[red]Unknown command: {args.command}[/red]")
        parser.print_help()


if __name__ == "__main__":
    main() 