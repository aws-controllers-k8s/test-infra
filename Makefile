SHELL := /bin/bash # Use bash syntax

PROW_JOBS_PATH="./prow/jobs"

AWS_SERVICE=$(shell echo $(SERVICE) | tr '[:upper:]' '[:lower:]')

.PHONY: build-prow-jobs

# Assumes python3 is installed as default python on the host.
build-prow-jobs: ## Compiles the Prow jobs
	@pushd "$(PROW_JOBS_PATH)" 1>/dev/null; \
	python jobs_factory.py && echo "Success! Prowjobs available at $(PROW_JOBS_PATH)/jobs.yaml" || \
	echo "Error while generating Prowjobs"; \
	popd 1>/dev/null

kind-test: export PRESERVE = true
kind-test: export LOCAL_MODULES = false
kind-test: ## Run functional tests for SERVICE with ACK_ROLE_ARN
	@./scripts/kind-build-test.sh $(AWS_SERVICE)

local-kind-test: export PRESERVE = true
local-kind-test: export LOCAL_MODULES = true
local-kind-test: ## Run functional tests for SERVICE with ACK_ROLE_ARN allowing local modules
	@./scripts/kind-build-test.sh $(AWS_SERVICE)

delete-all-kind-clusters:	## Delete all local kind clusters
	@kind delete clusters --all

help:           ## Show this help.
	@grep -F -h "##" $(MAKEFILE_LIST) | grep -F -v grep | sed -e 's/\\$$//' \
		| awk -F'[:#]' '{print $$1 = sprintf("%-30s", $$1), $$4}'