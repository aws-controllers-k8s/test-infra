FROM docker.io/library/python:3.8-alpine

# Persist build arguments into environment variables
ARG AWS_SERVICE
ARG E2E_GIT_REF=main
ARG CONTROLLER_E2E_PATH=./${AWS_SERVICE}-controller/test/e2e
ENV AWS_SERVICE ${AWS_SERVICE}
ENV PYTHONPATH ${CONTROLLER_E2E_PATH}
ENV CONTROLLER_E2E_PATH ${CONTROLLER_E2E_PATH}

WORKDIR /soak
# Install dependencies for soak test environment
RUN apk add --no-cache git bash gcc libc-dev curl \
    && curl -L -s https://github.com/mikefarah/yq/releases/download/v4.9.6/yq_linux_amd64 --output /usr/bin/yq \
    && chmod +x /usr/bin/yq

# Copy the script to run soak tests.
COPY run_soak_test.sh .
RUN chmod +x run_soak_test.sh

COPY default_soak_config.yaml .

# Checkout the controller repository where e2e tests are present.
# Soak test run consists of multiple runs of these e2e tests.
RUN git clone https://github.com/aws-controllers-k8s/${AWS_SERVICE}-controller.git -b ${E2E_GIT_REF} --depth 1
RUN cd ${AWS_SERVICE}-controller/test/e2e \
    && pip install -r requirements.txt

ENTRYPOINT ["bash", "-c", "./run_soak_test.sh"]
