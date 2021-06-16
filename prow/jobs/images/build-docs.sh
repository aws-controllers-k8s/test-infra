#!/usr/bin/env bash

set -eo pipefail

if [ -z "${GITHUB_TOKEN}" ]; then
    >&2 echo "build-docs.sh] GITHUB_TOKEN not specified. Required for pushing to GH pages."
    exit 1
fi

if [ -z "${GITHUB_ACTOR}" ]; then
    echo "build-docs.sh] GITHUB_ACTOR not specified. Defaulting to 'ack-bot'"
    GITHUB_ACTOR="ack-bot"
fi

GITHUB_SRC_GOPATH="${GOPATH}/src/github.com/"
COMMUNITY_REPO="aws-controllers-k8s/community"

COMMUNITY_PATH="${GITHUB_SRC_GOPATH}/${COMMUNITY_REPO}"
DOCS_PATH="${COMMUNITY_PATH}/docs"
export CONFIG_FILE="docs/mkdocs.yml"

# Generate new reference sources

pushd $DOCS_PATH 1> /dev/null

echo "build-docs.sh] 📝 Installing requirements file... "
pip install -r requirements.txt

echo -n "build-docs.sh] 📄 Generating reference files... "
python ./scripts/gen_reference.py
echo "Done!"

popd 1> /dev/null

# Deploy to GH pages

pushd $COMMUNITY_PATH 1> /dev/null

remote_repo="https://x-access-token:${GITHUB_TOKEN}@${GITHUB_DOMAIN:-"github.com"}/${COMMUNITY_REPO}.git"

git config --global user.name "${GITHUB_ACTOR}"
git config --global user.email "${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
git remote add origin "${remote_repo}"

echo "build-docs.sh] 📨 Deploying to Github pages... "
mkdocs gh-deploy --config-file "${CONFIG_FILE}" --force
echo "Done!"

popd 1> /dev/null