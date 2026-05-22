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
COMMUNITY_REPO="${COMMUNITY_REPO:-"${TEST_INFRA_ORG}/community"}"

DEFAULT_COMMUNITY_PATH="${GITHUB_SRC_GOPATH}${COMMUNITY_REPO}"
COMMUNITY_PATH="${COMMUNITY_PATH:-$DEFAULT_COMMUNITY_PATH}"
DOCS_PATH="${COMMUNITY_PATH}/docs"
GEN_SERVICES_FLAGS="${GEN_SERVICES_FLAGS:-"--debug"}"

# Generate new reference sources

pushd $DOCS_PATH 1> /dev/null

echo "build-docs.sh] 📝 Installing requirements file... "
pip install -r requirements.txt

# Install acktools from the local test-infra checkout (overrides pinned version in requirements.txt)
TEST_INFRA_PATH="${GITHUB_SRC_GOPATH}${TEST_INFRA_ORG}/${TEST_INFRA_REPO:-ack-test-infra}"
if [ -d "${TEST_INFRA_PATH}/tools" ]; then
    echo "build-docs.sh] 📦 Installing acktools from local test-infra..."
    pip install --no-deps --force-reinstall "${TEST_INFRA_PATH}/tools"
fi

echo -n "build-docs.sh] 📄 Generating services page... "
python3 ./scripts/gen_services.py ${GEN_SERVICES_FLAGS}
echo "Done!"

echo -n "build-docs.sh] 📄 Generating reference files... "
python3 ./scripts/gen_reference.py
echo "Done!"

echo "build-docs.sh] 🛠️ Building the Hugo site... "
npm install
npm run postinstall
npm run build
echo "Done!"

remote_repo="https://x-access-token:${GITHUB_TOKEN}@${GITHUB_DOMAIN:-"github.com"}/${COMMUNITY_REPO}.git"

user_email="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_ID}" ]; then
    user_email="${GITHUB_EMAIL_ID}+${user_email}"
fi

echo "build-docs.sh] 📨 Deploying to Github pages... "
short_sha=$(git rev-parse --short HEAD)
./node_modules/.bin/gh-pages --dist "public" \
    -u "${GITHUB_ACTOR} <${user_email}>" -r "${remote_repo}" \
    -m "Deployed ${short_sha}"
echo "Done!"

popd 1> /dev/null