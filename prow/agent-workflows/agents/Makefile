# List available workflows
list-workflows:
	uv run --refresh python -m workflows list

# Add a resource using the role-based Planner -> Review -> Implement -> Review loop.
# make workflow-add-resource SERVICE=s3 RESOURCE=AccessPoint
workflow-add-resource:
	uv run --refresh python -m workflows resource-addition --service $(SERVICE) --resource $(RESOURCE)

# Run the offline (no-AWS) harness logic tests: verdict parser, graph edge
# conditions/loop bounds, e2e classifier, reporting, progress rendering.
test-offline:
	.venv/bin/python tests/test_roles_offline.py
