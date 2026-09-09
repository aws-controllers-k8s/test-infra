#!/bin/bash

set -eo pipefail


USAGE="
Usage:
  $(basename "$0")

Environment variables:
  GITHUB_ACTOR:        Name of the GitHub account creating the issues & PR.
  GITHUB_TOKEN:        Personal Access Token for '$GITHUB_ACTOR'
  GITHUB_ORG:          Name of the GitHub organization.
  GITHUB_EMAIL_PREFIX  Email prefix to use for GitHub commits.
  PR_TARGET_BRANCH:    Name of the GitHub branch where the PR should merge the
                       code. Defaults to 'main'
"

# Default values
SERVICE=""
RESOURCE=""
# Optional; forwarded to `python -m workflows` only when set so the entrypoint's
# own defaults (DEFAULT_MODEL_ID, auto-detected SDK version) still apply otherwise.
MODEL=""
AWS_SDK_VERSION=""
SCRIPT_NAME="prow-job.sh"



# Parse command line arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
    --service)
      SERVICE="$2"
      shift 2
      ;;
    --resource)
      RESOURCE="$2"
      shift 2
      ;;
    --model)
      MODEL="$2"
      shift 2
      ;;
    --aws-sdk-version)
      AWS_SDK_VERSION="$2"
      shift 2
      ;;
    *)
      # Unknown option
      shift
      ;;
  esac
done

# Validate that service and resource are set
if [ -z "$SERVICE" ]; then
  echo "Error: --service argument is required"
  exit 1
fi

if [ -z "$RESOURCE" ]; then
  echo "Error: --resource argument is required"
  exit 1
fi

DEFAULT_PR_TARGET_BRANCH="main"
PR_TARGET_BRANCH=${PR_TARGET_BRANCH:-$DEFAULT_PR_TARGET_BRANCH}
# Resolve the workflow package dir from this script's own location, NOT $(pwd):
# once the job declares extra_refs, Prow's decoration runs the entrypoint from a
# clonerefs checkout dir (an extra_ref) rather than the image's /app, so `pwd`
# would point at code-generator and `python -m workflows` would fail / import the
# wrong packages.
WORKFLOW_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOB_USER="prow"
SERVICE_REPO=$SERVICE-controller
ORG_REPO=$GITHUB_ORG/$SERVICE-controller
# Clone the controller fork into the same directory clonerefs placed the
# code-generator / runtime / test-infra extra_refs, so `make kind-test` (via
# scripts/controller-setup.sh) can resolve them as siblings
REPO_ROOT="$(dirname "${CODEGEN_DIR:-/home/$JOB_USER/go/src/github.com/aws-controllers-k8s/code-generator}")"
SERVICE_REPO_DIR="$REPO_ROOT/$SERVICE-controller"
LOCAL_GIT_BRANCH=$SERVICE-add-$RESOURCE
PR_SOURCE_BRANCH=$LOCAL_GIT_BRANCH

echo "$SCRIPT_NAME][INFO] Running resource-addition workflow for service: $SERVICE, resource: $RESOURCE"
echo "$SCRIPT_NAME][INFO] Target repository: $GITHUB_ORG/$SERVICE-controller"

USER_EMAIL="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_PREFIX}" ]; then
    USER_EMAIL="${GITHUB_EMAIL_PREFIX}+${USER_EMAIL}"
fi

# set the GitHub configuration for using GitHub cli.
git config --global user.name "${GITHUB_ACTOR}" >/dev/null
git config --global user.email "${USER_EMAIL}" >/dev/null

mkdir -p $REPO_ROOT && cd $REPO_ROOT

# Create a fork of the repository
echo "$SCRIPT_NAME][INFO] forking and cloning $GITHUB_ORG/$SERVICE_REPO... "
if ! gh repo fork "$GITHUB_ORG/$SERVICE_REPO" --clone=true >/dev/null; then
echo ""
echo "$SCRIPT_NAME][ERROR] failed to fork and clone $GITHUB_ORG/$SERVICE_REPO. Exiting "
exit 1
fi
echo "ok"

# Sync the fork's main with upstream before branching.
cd "$SERVICE_REPO_DIR"
gh repo sync "$GITHUB_ACTOR/$SERVICE_REPO" --branch main --force >/dev/null
git fetch origin main >/dev/null
git reset --hard origin/main >/dev/null

cd $WORKFLOW_DIR

# Point the workflow at the forked controller checkout this script later commits
# and pushes, so edits land in exactly that tree (otherwise CONTROLLER_DIR would
# default to the agents package cwd).
export CONTROLLER_DIR="$SERVICE_REPO_DIR"

# code-generator's `make build-controller` defaults the controller source path to
# <code-generator>/../<service>-controller, but the fork lives under a different
# parent ($REPO_ROOT) than the clonerefs-mounted deps. Point it explicitly at the
# forked checkout so code generation writes into the tree this script commits.
export SERVICE_CONTROLLER_SOURCE_PATH="$CONTROLLER_DIR"

# code-generator and ack-dev-skills are delivered to this pod as Prow extra_refs
# cloned by the clonerefs init container. The agent-plugin sets CODEGEN_DIR /
# ACK_DEV_SKILLS_DIR explicitly; default to the standard clonerefs paths here so a
# manual/local run still resolves them.
export CODEGEN_DIR="${CODEGEN_DIR:-/home/$JOB_USER/go/src/github.com/aws-controllers-k8s/code-generator}"
export ACK_DEV_SKILLS_DIR="${ACK_DEV_SKILLS_DIR:-/home/$JOB_USER/go/src/github.com/aws-controllers-k8s/ack-dev-skills}"

# Where the workflow writes the composed PR description; used for `gh pr create
# -F` below (falls back to a static body if the workflow didn't produce one).
export PR_BODY_FILE="${PR_BODY_FILE:-/tmp/ack-pr-body.md}"

# E2E setup, only when RUN_E2E=true (the agent-plugin sets it on the e2e pod). The
# workflow runs `make kind-test` in-process, so Docker and the sandbox test-role
# must be ready before `python -m workflows` is invoked below.
if [ "${RUN_E2E,,}" = "true" ]; then
  export AWS_REGION="${AWS_REGION:-us-west-2}"
  export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-$AWS_REGION}"

  # Start Docker-in-Docker, mirroring prow/jobs/images/wrapper.sh. The image
  # disables cgroupfs_mount and forces iptables-legacy so kind's nodes boot here.
  echo "$SCRIPT_NAME][INFO] RUN_E2E=true: starting Docker-in-Docker..."
  sysctl net.ipv6.conf.all.disable_ipv6=0 || true
  sysctl net.ipv6.conf.all.forwarding=1 || true
  modprobe -v ip6table_nat || true
  sed -i 's|ulimit -Hn|ulimit -n|' /etc/init.d/docker || true
  service docker start
  WAIT_N=0
  until docker ps -q >/dev/null 2>&1; do
    if [ "$WAIT_N" -ge 5 ]; then
      echo "$SCRIPT_NAME][ERROR] Docker daemon did not become ready; e2e cannot run"
      exit 1
    fi
    WAIT_N=$((WAIT_N + 1))
    echo "$SCRIPT_NAME][INFO] waiting for Docker (${WAIT_N})..."
    sleep "$WAIT_N"
  done
  echo "$SCRIPT_NAME][INFO] Docker-in-Docker ready"

  if ! { aws ecr-public get-login-password --region us-east-1 \
       | docker login --username AWS --password-stdin public.ecr.aws; } >/dev/null 2>&1; then
    echo "$SCRIPT_NAME][INFO] ECR Public login unavailable; continuing without it"
  fi

  ASSUMED_ROLE_ARN=$(aws ssm get-parameter --name /ack/prow/agent-e2e-role \
    --query Parameter.Value --output text) || {
    echo "$SCRIPT_NAME][ERROR] could not read /ack/prow/agent-e2e-role from SSM; e2e cannot run"
    exit 1
  }
  export ASSUMED_ROLE_ARN
  echo "$SCRIPT_NAME][INFO] Exported ASSUMED_ROLE_ARN for e2e"

  # The e2e harness runs the AWS CLI via `daws` — a nested container inside DinD
  # that cannot reach the EKS Pod Identity credential endpoint. Resolve this pod's
  # own (workflow-runner) Pod Identity credentials into static env vars so daws can
  # authenticate; the harness then assumes ASSUMED_ROLE_ARN from them for the tests.
  CREDS_ENV=$(aws configure export-credentials --format env) || {
    echo "$SCRIPT_NAME][ERROR] could not export static AWS credentials for daws; e2e cannot run"
    exit 1
  }
  eval "$CREDS_ENV"
  echo "$SCRIPT_NAME][INFO] Exported static AWS credentials for daws"
fi

# Run the workflow command
echo "$SCRIPT_NAME][INFO] Starting workflow"
# Forward optional args only when provided, so the workflow's own defaults apply
# otherwise (DEFAULT_MODEL_ID; SDK version auto-detected).
WORKFLOW_ARGS=(resource-addition --service "$SERVICE" --resource "$RESOURCE")
[ -n "$MODEL" ] && WORKFLOW_ARGS+=(--model "$MODEL")
[ -n "$AWS_SDK_VERSION" ] && WORKFLOW_ARGS+=(--aws-sdk-version "$AWS_SDK_VERSION")
python -m workflows "${WORKFLOW_ARGS[@]}"
echo "$SCRIPT_NAME][INFO]Resource addition workflow completed successfully"

cd $SERVICE_REPO_DIR

# Create a new branch
echo "$SCRIPT_NAME][INFO] Creating a new branch..."
git checkout -b $LOCAL_GIT_BRANCH >/dev/null

# Commit changes
echo "$SCRIPT_NAME][INFO] Committing changes..."
git add -A  >/dev/null
COMMIT_MSG="Add $RESOURCE to $SERVICE"
git commit -am "$COMMIT_MSG" >/dev/null

# Push changes to the forked repository
echo "$SCRIPT_NAME][INFO] Pushing changes to the forked repository..."
git push --force "https://$GITHUB_TOKEN@github.com/$GITHUB_ACTOR/$SERVICE_REPO.git" \
   "$LOCAL_GIT_BRANCH:$PR_SOURCE_BRANCH" &>/dev/null

# fetch all remotes to bring changes locally
git fetch --all >/dev/null
# set local branch to track origin(PR source)
git branch "$LOCAL_GIT_BRANCH" --set-upstream-to origin/"$PR_SOURCE_BRANCH" >/dev/null
# sync local branch with the origin, if there is a diff the gh pr command
# prompts for user input
git pull --rebase >/dev/null

echo "$SCRIPT_NAME][INFO] Creating a new pull request for $ORG_REPO , from $PR_SOURCE_BRANCH -> $PR_TARGET_BRANCH branch... "
# Prefer the workflow-composed PR description (summary + key design decisions);
# fall back to a static body if it wasn't produced.
if [ -s "$PR_BODY_FILE" ]; then
  PR_BODY_ARGS=(-F "$PR_BODY_FILE")
else
  PR_BODY_ARGS=(-b "ACK Agent changes adding $RESOURCE to $SERVICE-controller")
fi
if ! gh pr create -R "$ORG_REPO" -t "$COMMIT_MSG" "${PR_BODY_ARGS[@]}" -B "$PR_TARGET_BRANCH" >/dev/null ; then
  echo ""
  echo "gh.sh][ERROR] Failed to create pull request. Exiting... "
  exit 1
fi
echo "ok"

