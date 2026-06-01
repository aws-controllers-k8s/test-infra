#!/usr/bin/env bash

set -eo pipefail

SCRIPT_NAME="deploy-docs.sh"

if [ -z "${GITHUB_TOKEN}" ]; then
    >&2 echo "${SCRIPT_NAME}] GITHUB_TOKEN not specified. Required for pushing to GH pages."
    exit 1
fi

GITHUB_ACTOR="${GITHUB_ACTOR:-ack-bot}"
GITHUB_SRC_GOPATH="${GOPATH}/src/github.com"
DOCS_REPO_PATH="${GITHUB_SRC_GOPATH}/${TEST_INFRA_ORG}/docs"
CONTROLLERS_DIR="${GITHUB_SRC_GOPATH}/${TEST_INFRA_ORG}"

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

# Set up git for deploy
echo "${SCRIPT_NAME}] Setting up git..."
# Override origin to point to the correct org (clonerefs may set a different URL)
git remote set-url origin "https://${GITHUB_ACTOR}:${GITHUB_TOKEN}@github.com/${TEST_INFRA_ORG}/docs.git" 2>/dev/null || \
  git remote add origin "https://${GITHUB_ACTOR}:${GITHUB_TOKEN}@github.com/${TEST_INFRA_ORG}/docs.git"
git config --global user.name "${GITHUB_ACTOR}"
git config --global user.email "${GITHUB_ACTOR}@users.noreply.github.com"

# Deploy to GitHub Pages
# ORGANIZATION_NAME, PROJECT_NAME, DEPLOYMENT_BRANCH override docusaurus.config.js
echo "${SCRIPT_NAME}] Deploying to GitHub Pages (org: ${TEST_INFRA_ORG})..."
ORGANIZATION_NAME="${TEST_INFRA_ORG}" PROJECT_NAME="docs" DEPLOYMENT_BRANCH="gh-pages" \
  GIT_USER="${GITHUB_ACTOR}" GIT_PASS="${GITHUB_TOKEN}" npm run deploy

echo "${SCRIPT_NAME}] Done!"
