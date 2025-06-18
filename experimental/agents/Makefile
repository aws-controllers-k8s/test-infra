# Run the agent CLI
run-builder:
	uv run --refresh python -m ack_builder_agent

run-generator:
	uv run --refresh python -m ack_generator_agent

run-model:
	uv run --refresh python -m ack_model_agent

# List available workflows
list-workflows:
	uv run --refresh python -m workflows list

# Add a resource using workflow
# make workflow-add-resource SERVICE=s3 RESOURCE=AccessPoint
workflow-add-resource:
	uv run --refresh python -m workflows resource-addition --service $(SERVICE) --resource $(RESOURCE)
