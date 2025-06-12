FROM python:3.12-slim

ARG GOPROXY=https://proxy.golang.org|direct
ENV GOPROXY=${GOPROXY}

ARG GO_VERSION=1.24.3
ENV GO_VERSION=${GO_VERSION}

# Create non-root user with home directory
RUN groupadd -r appuser && useradd -r -g appuser -m appuser

# Set environment variables
ENV GOPATH=/home/appuser/go \
    GO111MODULE=on \
    PATH=/home/appuser/go/bin:/usr/local/go/bin:${PATH} \
    PYTHONUNBUFFERED=1

# Set working directory
WORKDIR /app

# Install git and dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    make \
    curl \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

RUN echo "Installing Go ..." \
    && export GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"\
    && curl -fsSL "https://storage.googleapis.com/golang/${GO_TARBALL}" --output "${GO_TARBALL}" \
    && tar xzf "${GO_TARBALL}" -C /usr/local \
    && rm "${GO_TARBALL}"

RUN echo "Installing yq ... " \
    && curl -L -s https://github.com/mikefarah/yq/releases/download/v4.31.2/yq_linux_amd64 --output /usr/bin/yq \
    && chmod +x /usr/bin/yq

RUN echo "Installing Helm ..." \
    && export HELM_URL="https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3" \
    && curl -fsSL ${HELM_URL} | bash

# Create Go directories with proper permissions
RUN mkdir -p "${GOPATH}/bin" "${GOPATH}/src" "${GOPATH}/pkg" \
    && chown -R appuser:appuser /home/appuser

# Install uv package manager
RUN pip install --no-cache-dir uv

# Copy project files
COPY . .

# Install dependencies using uv and set permissions
RUN uv pip install --system . && \
    chown -R appuser:appuser /app

# Switch to non-root user
USER appuser

# Default command to run the generator agent
ENTRYPOINT ["python", "-m", "ack_generator_agent"]