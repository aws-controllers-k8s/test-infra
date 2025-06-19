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
"""ACK Resource Addition Workflow - Agent Orchestrator."""

import os
import yaml
from typing import Optional, List
from dataclasses import dataclass
from pathlib import Path

from utils.bedrock import create_enhanced_agent
from utils.settings import settings
from ack_model_agent.prompt import ACK_MODEL_AGENT_SYSTEM_PROMPT
from ack_generator_agent.prompts import ACK_GENERATOR_SYSTEM_PROMPT

from ack_model_agent.tools import (
    save_operations_catalog,
    save_field_catalog,
    save_operation_analysis,
    save_error_catalog,
    save_resource_characteristics,
    query_knowledge_base,
)
from ack_generator_agent.tools import (
    add_memory,
    build_controller_agent,
    error_lookup,
    list_all_memories,
    load_all_analysis_data,
    read_service_generator_config,
    save_error_solution,
    search_codegen_knowledge,
    search_memories,
    update_service_generator_config,
)

@dataclass
class ResourceAdditionInput:
    """Input for the ACK Resource Addition Workflow."""
    service: str
    resource: str
    aws_sdk_version: str = "v1.32.6"
    timeout_minutes: int = 30

@dataclass  
class ResourceAdditionOutput:
    """Output from the ACK Resource Addition Workflow."""
    success: bool
    service: str
    resource: str
    build_logs: Optional[str] = None
    error_message: Optional[str] = None
    config_changes: Optional[str] = None

class ACKResourceWorkflow:
    """ACK Resource Addition Workflow that runs individual agents as separate processes."""
    
    def __init__(self):
        """Initialize the workflow orchestrator."""
        pass
    
    def _load_supported_services(self) -> List[str]:
        """Load the list of supported AWS services from jobs_config.yaml."""
        try:
            # Go up to test-infra root, then to prow/jobs/jobs_config.yaml
            config_path = Path(__file__).parent.parent.parent.parent / "prow" / "jobs" / "jobs_config.yaml"
            
            with open(config_path, 'r') as f:
                config = yaml.safe_load(f)
                
            return config.get('aws_services', [])
        except Exception as e:
            print(f"\n\033[93m⚠️  Warning: Could not load supported services list: {e}\033[0m")
            return []
    
    def _validate_service(self, service: str) -> tuple[bool, str]:
        """Validate that the service is supported in ACK infrastructure."""
        supported_services = self._load_supported_services()
        
        if not supported_services:
            return False, "Could not load supported services list from jobs_config.yaml"
        
        if service.lower() in [s.lower() for s in supported_services]:
            return True, f"Service '{service}' is supported"
        else:
            available_services = ", ".join(sorted(supported_services))
            return False, f"Service '{service}' is not supported. Available services: {available_services}"

    def _create_model_agent(self):
        """Create a fresh Model Agent instance."""
        return create_enhanced_agent(
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

    def _create_generator_agent(self):
        """Create a fresh Generator Agent instance."""
        return create_enhanced_agent(
            tools=[
                load_all_analysis_data,
                error_lookup,
                read_service_generator_config,
                build_controller_agent,
                update_service_generator_config,
                save_error_solution,
                add_memory,
                search_memories,
                list_all_memories,
                search_codegen_knowledge,
            ],
            system_prompt=ACK_GENERATOR_SYSTEM_PROMPT,
        )



    def _get_analysis_files_directory(self, service: str, resource: str) -> str:
        """Get the directory where analysis files are stored."""
        return os.path.join(settings.model_logs_dir, service, resource)

    def _check_analysis_files_exist(self, service: str, resource: str) -> tuple[bool, list[str]]:
        """Check if all required analysis files exist in the correct directory."""
        analysis_dir = self._get_analysis_files_directory(service, resource)
        analysis_files = [
            "operations_catalog.json",
            "field_catalog.json", 
            "operation_analysis.json",
            "error_catalog.json",
            "characteristics.json"
        ]
        
        missing_files = []
        for file in analysis_files:
            file_path = os.path.join(analysis_dir, file)
            if not os.path.exists(file_path):
                missing_files.append(file_path)
        
        return len(missing_files) < 5, missing_files

    async def _run_model_agent(self, service: str, resource: str) -> tuple[bool, str]:
        """Run the Model Agent to analyze the resource."""
        print(f"\n\n\033[94m🔍 Step 1: Running Model Agent for {service} {resource}\033[0m\n")
        
        try:
            model_agent = self._create_model_agent()
            prompt = f"""Analyze the AWS {service} service {resource} resource following the complete workflow:

1. Execute exactly 2 strategic knowledge base queries
2. Extract comprehensive AWS resource information  
3. Create structured data for all 5 analysis files
4. Save all analysis data using the tools

Focus on creating ONE comprehensive operation_analysis dictionary with ALL operations for {resource}, not individual operation calls.

Service: {service}
Resource: {resource}"""
            
            response = model_agent(prompt)
            
            # Check if analysis files were created successfully in the correct directory
            success, missing_files = self._check_analysis_files_exist(service, resource)
            
            if success:
                analysis_dir = self._get_analysis_files_directory(service, resource)
                print(f"\n\033[92m✅ Model Agent completed successfully - analysis files created in {analysis_dir}\033[0m\n")
                return True, str(response)
            else:
                print(f"\n\033[91m❌ Model Agent failed - missing analysis files: {missing_files}\033[0m\n")
                return False, f"Analysis files not created in expected location. Missing: {missing_files}. Response: {response}"
                
        except Exception as e:
            print(f"\n\033[91m❌ Model Agent failed with error: {e}\033[0m\n")
            return False, str(e)

    async def _run_generator_agent(self, service: str, resource: str, aws_sdk_version: str) -> tuple[bool, str]:
        """Run the Generator Agent to create configuration and build controller."""
        print(f"\n\033[93m⚙️  Step 2: Running Generator Agent for {service} {resource}\033[0m\n")
        
        try:
            # Create fresh agent instance to avoid token overflow
            generator_agent = self._create_generator_agent()
            
            prompt = f"""Load analysis data for {service} {resource}, read current generator.yaml configuration, generate optimized generator.yaml configuration for the {resource} resource, update the generator.yaml file, and then build the controller using build_controller_agent with AWS SDK {aws_sdk_version}.

Follow the complete process:
1. Load analysis data
2. Read current generator.yaml
3. Generate new configuration 
4. Update generator.yaml file
5. Build controller using build_controller_agent and monitor build
6. If build fails, fix errors and try building once more

Handle any build errors by fixing the configuration and attempting to build again."""
            
            response = generator_agent(prompt)
            
            # Check for success indicators in response
            response_str = str(response)
            success = (
                "success:" in response_str.lower() or
                "build completed successfully" in response_str.lower() or
                "controller built successfully" in response_str.lower() or
                "successfully built" in response_str.lower()
            )
            
            if success:
                print(f"\n\033[92m✅ Generator Agent completed - configuration updated and controller built\033[0m\n")
                return True, str(response)
            else:
                print(f"\n\033[93m⚠️  Generator Agent completed but build may have had issues\033[0m\n")
                return False, str(response)
                
        except Exception as e:
            print(f"\n\033[91m❌ Generator Agent failed with error: {e}\033[0m\n")
            return False, str(e)



    async def _save_results(self, service: str, resource: str, behavior_learned: str, final_message: str) -> bool:
        """Save workflow results to memory."""
        print(f"\n\n\033[97m💾 Step 3: Saving results to memory\033[0m\n")
        
        try:
            # Create fresh agent instance to avoid token overflow
            generator_agent = self._create_generator_agent()
            
            memory_prompt = f"""Save the following workflow results to memory:

Service: {service}
Resource: {resource}
Behavior Learned: {behavior_learned}
Final Status: {final_message}

Use add_memory to store this information for future reference."""
            
            response = generator_agent(memory_prompt)
            print(f"\n\033[92m✅ Results saved to memory\033[0m\n")
            return True
            
        except Exception as e:
            print(f"\n\033[91m❌ Failed to save results: {e}\033[0m\n")
            return False

    async def run(self, input_data: ResourceAdditionInput) -> ResourceAdditionOutput:
        """Execute the complete workflow by running individual agents."""
        try:
            print(f"\n\033[95m🚀 Starting ACK Resource Addition Workflow\033[0m")
            print(f"   Service: {input_data.service}")
            print(f"   Resource: {input_data.resource}")
            print(f"   AWS SDK: {input_data.aws_sdk_version}")
            
            # Step 0: Validate service is supported
            print(f"\n\033[96m🔍 Validating service '{input_data.service}' is supported...\033[0m")
            service_valid, validation_message = self._validate_service(input_data.service)
            
            if not service_valid:
                print(f"\n\033[91m❌ Service validation failed\033[0m\n")
                return ResourceAdditionOutput(
                    success=False,
                    service=input_data.service,
                    resource=input_data.resource,
                    error_message=f"Service validation failed: {validation_message}",
                )
            else:
                print(f"\n\033[92m✅ {validation_message}\033[0m")
            
            # Step 1: Run Model Agent
            model_success, model_response = await self._run_model_agent(input_data.service, input_data.resource)
            if not model_success:
                return ResourceAdditionOutput(
                    success=False,
                    service=input_data.service,
                    resource=input_data.resource,
                    error_message=f"Model Agent failed: {model_response}",
                )
            
            # Step 2: Run Generator Agent
            generator_success, generator_response = await self._run_generator_agent(
                input_data.service, 
                input_data.resource, 
                input_data.aws_sdk_version
            )
            
            if not generator_success:
                # Save failure results
                await self._save_results(
                    input_data.service, 
                    input_data.resource, 
                    f"Failed to generate configuration and build controller for {input_data.resource}. Error: {generator_response}",
                    "Failed"
                )
                return ResourceAdditionOutput(
                    success=False,
                    service=input_data.service,
                    resource=input_data.resource,
                    error_message=f"Generator Agent failed: {generator_response}",
                )
            
            # Step 3: Save success results
            await self._save_results(
                input_data.service, 
                input_data.resource, 
                f"Successfully generated configuration and built controller for {input_data.resource}. Configuration optimized based on analysis data.",
                "Success"
            )
            
            # Final status
            print(f"\n\033[92m🎉 Workflow completed successfully!\033[0m")
            print(f"   {input_data.resource} added to {input_data.service} controller")
            print()
            
            return ResourceAdditionOutput(
                success=generator_success,
                service=input_data.service,
                resource=input_data.resource,
                build_logs=generator_response,
                config_changes=generator_response if generator_success else None,
                error_message=None,
            )
            
        except Exception as e:
            error_msg = f"Workflow execution failed: {str(e)}"
            print(f"\n\033[91m💥 {error_msg}\033[0m\n")
            
            return ResourceAdditionOutput(
                success=False,
                service=input_data.service,
                resource=input_data.resource,
                error_message=error_msg,
            )


def create_ack_resource_workflow() -> ACKResourceWorkflow:
    """Create and return a new ACK Resource Workflow instance."""
    return ACKResourceWorkflow() 