#!/usr/bin/env bash

set -eo pipefail

SCRIPT_NAME="deploy-docs.sh"

if [ -z "${GITHUB_TOKEN}" ]; then
    >&2 echo "${SCRIPT_NAME}] GITHUB_TOKEN not specified. Required for pushing to GH pages."
    exit 1
fi

GITHUB_ACTOR="${GITHUB_ACTOR:-ack-bot}"
GITHUB_SRC_GOPATH="${GOPATH}/src/github.com"
DOCS_REPO_PATH="${GITHUB_SRC_GOPATH}/aws-controllers-k8s/docs"
CONTROLLERS_DIR="${GITHUB_SRC_GOPATH}/aws-controllers-k8s"

echo "${SCRIPT_NAME}] Starting website build and deploy..."
echo "${SCRIPT_NAME}] Docs repo: ${DOCS_REPO_PATH}"
echo "${SCRIPT_NAME}] Controllers dir: ${CONTROLLERS_DIR}"

cd "${DOCS_REPO_PATH}"

# Generate service data from controller repos
echo "${SCRIPT_NAME}] Running make generate..."
CONTROLLERS_DIR="${CONTROLLERS_DIR}" make generate

# Build the Docusaurus site
echo "${SCRIPT_NAME}] Running make build..."
cd website
npm install
npm run build

# Set up git remote for Docusaurus deploy (Prow clone doesn't set up 'origin')
echo "${SCRIPT_NAME}] Setting up git remote..."
git remote add origin "https://github.com/aws-controllers-k8s/docs.git" 2>/dev/null || true

# Deploy to GitHub Pages
echo "${SCRIPT_NAME}] Deploying to GitHub Pages..."
GIT_USER="${GITHUB_ACTOR}" GIT_PASS="${GITHUB_TOKEN}" npm run deploy

echo "${SCRIPT_NAME}] Done!"
