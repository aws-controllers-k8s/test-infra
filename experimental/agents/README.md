# AWS Controllers for Kubernetes (ACK) Agent


## Overview

The ACK Agent provides a conversational interface to help you work with AWS Controllers for Kubernetes:

- Clone and manage ACK repositories (code-generator, runtime, service controllers)
- Build service controllers for AWS services
- Read build logs and check build status
- Examine service controller configurations and API operations

## Prerequisites

1. Python 3.10 or higher
2. [uv](https://github.com/astral-sh/uv) - Modern Python package manager
3. AWS account with access to Amazon Bedrock
4. Claude 3.7 Sonnet model access enabled in your AWS account
5. AWS credentials configured in your environment
6. Opensearch Vector Database for memory agent
7. Bedrock Knowledgebase (with ACK Codegen)

## Installation

This project uses `uv` for dependency management. If you don't have `uv` installed, you can install it following the [official installation instructions](https://github.com/astral-sh/uv#installation).

### Setting up the project

1. OpenSearch Vector Store

## Usage

Run the builder agent:

```bash
# Using make
make run-builder

# Or directly
uv run python -m ack_builder_agent
```

Run the generator agent:

```bash
# Using make
make run-generator

# Or directly
uv run python -m ack_generator_agent
```

You can provide command-line arguments:

```bash
uv run python -m ack_generator_agent --region us-east-1 --temperature 0.5 --debug
```

### Available Arguments

- `--region`: AWS region for Bedrock (default: us-west-2)
- `--model`: Model ID for Claude on Bedrock (default: us.anthropic.claude-3-7-sonnet-20250219-v1:0)
- `--temperature`: Temperature for model generation (default: 0.2)
- `--debug`: Enable debug logging


## Available Tools

The agents provide several tools for working with ACK:

1. `get_latest_version` - Get the latest version of an ACK repository
2. `build_controller` - Build a controller for a specific AWS service (builder agent)
3. `read_build_log` - Read the logs from a controller build (builder agent)
4. `sleep` - Pause execution for a specified time (builder agent)
5. `read_service_generator_config` - Read a service's generator configuration (generator agent)
6. `read_service_model` - Read the AWS service model (generator agent)
7. `build_controller_agent` - Delegate the controller build process to the builder agent (generator agent)

## License

This project is licensed under the Apache License 2.0 - see the LICENSE file for details. 