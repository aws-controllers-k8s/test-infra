SHELL := /bin/bash # Use bash syntax

PROW_JOBS_PATH="./prow/jobs"

AWS_SERVICE=$(shell echo $(SERVICE) | tr '[:upper:]' '[:lower:]')

.PHONY: build-prow-jobs

# Assumes python3 is installed as default python on the host.
build-prow-jobs: ## Compiles the Prow jobs
	@pushd "$(PROW_JOBS_PATH)" 1>/dev/null; \
	python generator.py jobs -o jobs.yaml && \
	echo "Success! Prowjobs available at $(PROW_JOBS_PATH)/jobs.yaml" || \
	echo "Error while generating Prowjobs"; \
	popd 1>/dev/null

build-label-config:
	@pushd "$(PROW_JOBS_PATH)" 1>/dev/null; \
	python generator.py labels -o labels.yaml && \
	echo "Success! Configuration file available at $(PROW_JOBS_PATH)/labels.yaml" || \
	echo "Error while generating label_sync config"; \
	popd 1>/dev/null

gen-all: build-prow-jobs build-label-config

kind-test: ## Run functional tests for SERVICE
	@AWS_SERVICE=$(AWS_SERVICE) ./scripts/run-e2e-tests.sh

kind-helm-test: ## Run the Helm tests for SERVICE
	@AWS_SERVICE=$(AWS_SERVICE) ./scripts/run-helm-tests.sh

test-recommended-policy:
	@AWS_SERVICE=$(AWS_SERVICE) source ./scripts/iam-policy-test-runner.sh && assert_iam_policies

delete-all-kind-clusters:	## Delete all local kind clusters
	@kind delete clusters --all
	@rm -rf build/*

help:           ## Show this help.
	@grep -F -h "##" $(MAKEFILE_LIST) | grep -F -v grep | sed -e 's/\\$$//' \
		| awk -F'[:#]' '{print $$1 = sprintf("%-30s", $$1), $$4}'
