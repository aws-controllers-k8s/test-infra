FROM python:3.8-alpine

# Persist build arguments into environment variables
ARG AWS_SERVICE
ENV AWS_SERVICE ${AWS_SERVICE}

# Path to the service e2e test directory in the build context
ARG CONTROLLER_E2E_PATH=./${AWS_SERVICE}-controller/test/e2e

# Path to the test-infra directory in the build context
ARG TEST_INFRA_PATH=./test-infra

# Destination path to copy aws-web-identity-token
ARG WEB_IDENTITY_TOKEN_DEST_PATH=/root/web-identity-token

# Mirror the e2e directory structure as the controller
WORKDIR /${AWS_SERVICE}-controller/tests/e2e
ENV PYTHONPATH=/${AWS_SERVICE}-controller/tests/e2e

RUN apk add --no-cache git bash gcc libc-dev

# Install python dependencies
COPY ${CONTROLLER_E2E_PATH}/requirements.txt .
RUN pip install -r requirements.txt

COPY ${CONTROLLER_E2E_PATH} .
RUN mkdir -p $HOME/.kube

# Copy the runner script
COPY ${TEST_INFRA_PATH}/scripts/run-tests.sh .

COPY ${TEST_INFRA_PATH}/scripts/web-identity-token ${WEB_IDENTITY_TOKEN_DEST_PATH}

# Run the tests
ENTRYPOINT ["./run-tests.sh"]
CMD ["$AWS_SERVICE"]