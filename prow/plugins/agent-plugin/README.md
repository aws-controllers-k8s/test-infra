# Prow Workflow Agent

A Prow external plugin that triggers AI workflows based on GitHub issue comments.

## Overview

The Workflow Agent listens for `/agent` commands in GitHub issues and automatically creates ProwJobs to execute predefined workflows.

## Usage

Comment on any GitHub issue with:
```
/agent <workflow-name> [key=value ...] [--timeout 30m]
```

## Configuration

### Workflows
Define workflows in `config/workflows.yaml`:
```yaml
workflows:
  ack_resource_workflow:
    description: "ACK resource addition workflow"
    image: "086987147623.dkr.ecr.us-west-2.amazonaws.com/workflow-agent:v1.0.7"
    command: ["python", "-m", "workflows.ack_resource_prow"]
    required_args: ["service", "resource"]
    optional_args: []
    timeout: "30m"
    resources:
      cpu: "2"
      memory: "4Gi"
```

**Configure Prow Hook:**
   Add to `plugins.yaml`:
   ```yaml
   external_plugins:
     your-org/your-repo:
     - name: workflow-agent
       endpoint: http://workflow-agent.prow:8080/tamer
       events:
       - issue_comment
       - issues
   ```

## Architecture

- **GitHub** → **Prow Hook** → **Workflow Agent** → **ProwJob** → **Kubernetes**
