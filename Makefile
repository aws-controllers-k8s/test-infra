SHELL := /bin/bash # Use bash syntax

PROW_JOBS_PATH="./prow/jobs"

.PHONY: build-prow-jobs

# Assumes python3 is installed as default python on the host.
build-prow-jobs:
	@pushd "$(PROW_JOBS_PATH)" 1>/dev/null; \
	python jobs_factory.py; \
	echo "Success! Prowjobs available at $(PROW_JOBS_PATH)/jobs.yaml"; \
	popd 1>/dev/null
