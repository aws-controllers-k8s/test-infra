#!/bin/bash

set -eo pipefail


USAGE="
Usage:
  $(basename "$0")

Environment variables:
  WORKFLOW_NAME:       Selected workflow name (add-resource, add-field or
                       address-feedback).
  GITHUB_ACTOR:        Name of the GitHub account creating the issues & PR.
  GITHUB_TOKEN:        Personal Access Token for '$GITHUB_ACTOR'
  GITHUB_ORG:          Name of the GitHub organization.
  GITHUB_EMAIL_PREFIX  Email prefix to use for GitHub commits.
  PR_TARGET_BRANCH:    Name of the GitHub branch where the PR should merge the
                       code. Defaults to 'main'

address-feedback only (set by the agent-plugin from the review event):
  ISSUE_NUMBER:        Number of the pull request that was reviewed.
  REPO_OWNER:          Owner of the pull request's repository.
  REPO_NAME:           Name of the pull request's repository.
  --review-id:         ID of the review that started the run.
  --review-sha:        Commit the review was made against.
  --team-members:      Comma-separated logins whose feedback is included.
"

# Default values
SERVICE=""
RESOURCE=""
FIELD=""
REVIEW_ID=""
REVIEW_SHA=""
TEAM_MEMBERS=""
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
    --field)
      FIELD="$2"
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
    --review-id)
      REVIEW_ID="$2"
      shift 2
      ;;
    --review-sha)
      REVIEW_SHA="$2"
      shift 2
      ;;
    --team-members)
      TEAM_MEMBERS="$2"
      shift 2
      ;;
    *)
      # Unknown option
      shift
      ;;
  esac
done

# Resolve the workflow package dir from this script's own location. Dispatch from
# the workflow selected by the plugin instead of inferring it from optional data.
WORKFLOW_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALIDATION_ARGS=(--service "$SERVICE" --resource "$RESOURCE")
[ -n "$AWS_SDK_VERSION" ] && VALIDATION_ARGS+=(--aws-sdk-version "$AWS_SDK_VERSION")
case "${WORKFLOW_NAME:-}" in
  add-resource)
    if [ -n "$FIELD" ]; then
      echo "$SCRIPT_NAME][ERROR] --field is not supported by add-resource" >&2
      exit 2
    fi
    WORKFLOW_COMMAND="resource-addition"
    WORKFLOW_LABEL="resource addition"
    LOCAL_GIT_BRANCH="$SERVICE-add-$RESOURCE"
    COMMIT_MSG="Add $RESOURCE to $SERVICE"
    ;;
  add-field)
    if [ -z "$FIELD" ]; then
      echo "$SCRIPT_NAME][ERROR] --field is required by add-field" >&2
      exit 2
    fi
    VALIDATION_ARGS+=(--field "$FIELD" --require-field)
    WORKFLOW_COMMAND="field-addition"
    WORKFLOW_LABEL="field addition"
    LOCAL_GIT_BRANCH="$SERVICE-add-$RESOURCE-$FIELD"
    COMMIT_MSG="Add $FIELD field to $RESOURCE"
    ;;
  address-feedback)
    # The plugin derives service, resource and field from the PR's head
    # branch; the branch is checked against them after the PR is fetched.
    [ -n "$FIELD" ] && VALIDATION_ARGS+=(--field "$FIELD")
    if ! [[ "${ISSUE_NUMBER:-}" =~ ^[0-9]+$ && "$REVIEW_ID" =~ ^[0-9]+$ ]]; then
      echo "$SCRIPT_NAME][ERROR] ISSUE_NUMBER and --review-id must be numbers" >&2
      exit 2
    fi
    if ! [[ "$REVIEW_SHA" =~ ^[0-9a-f]{40}$ ]]; then
      echo "$SCRIPT_NAME][ERROR] --review-sha must be a full commit SHA" >&2
      exit 2
    fi
    if ! [[ "${REPO_OWNER:-}" =~ ^[A-Za-z0-9-]+$ && "${REPO_NAME:-}" == "$SERVICE-controller" ]]; then
      echo "$SCRIPT_NAME][ERROR] REPO_OWNER/REPO_NAME must name the $SERVICE-controller repository" >&2
      exit 2
    fi
    if [ -z "$TEAM_MEMBERS" ]; then
      echo "$SCRIPT_NAME][ERROR] --team-members is required by address-feedback" >&2
      exit 2
    fi
    WORKFLOW_COMMAND="address-feedback"
    WORKFLOW_LABEL="address feedback"
    LOCAL_GIT_BRANCH="$SERVICE-add-$RESOURCE${FIELD:+-$FIELD}"
    COMMIT_MSG="Address review feedback"
    ;;
  *)
    echo "$SCRIPT_NAME][ERROR] unsupported WORKFLOW_NAME: ${WORKFLOW_NAME:-<unset>}" >&2
    exit 2
    ;;
esac

# Validate invocation values before using them in repository paths or Git
# commands. This keeps one structural-validation implementation for the shell
# wrapper and workflow adapters.
python "$WORKFLOW_DIR/workflows/validation.py" "${VALIDATION_ARGS[@]}"

DEFAULT_PR_TARGET_BRANCH="main"
PR_TARGET_BRANCH=${PR_TARGET_BRANCH:-$DEFAULT_PR_TARGET_BRANCH}
# The workflow directory was resolved before input validation so decorated Prow
# jobs do not depend on their initial working directory.
JOB_USER="prow"
SERVICE_REPO=$SERVICE-controller
ORG_REPO=$GITHUB_ORG/$SERVICE-controller
# Clone the controller fork into the same directory clonerefs placed the
# code-generator / runtime / test-infra extra_refs, so `make kind-test` (via
# scripts/controller-setup.sh) can resolve them as siblings
REPO_ROOT="$(dirname "${CODEGEN_DIR:-/home/$JOB_USER/go/src/github.com/aws-controllers-k8s/code-generator}")"
SERVICE_REPO_DIR="$REPO_ROOT/$SERVICE-controller"
PR_SOURCE_BRANCH="$LOCAL_GIT_BRANCH"

echo "$SCRIPT_NAME][INFO] Running $WORKFLOW_LABEL workflow for service: $SERVICE, resource: $RESOURCE${FIELD:+, field: $FIELD}"
echo "$SCRIPT_NAME][INFO] Target repository: $GITHUB_ORG/$SERVICE-controller"

USER_EMAIL="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_PREFIX}" ]; then
    USER_EMAIL="${GITHUB_EMAIL_PREFIX}+${USER_EMAIL}"
fi

# set the GitHub configuration for using GitHub cli.
git config --global user.name "${GITHUB_ACTOR}" >/dev/null
git config --global user.email "${USER_EMAIL}" >/dev/null

mkdir -p "$REPO_ROOT" && cd "$REPO_ROOT"

# pr_comment posts a comment on the reviewed pull request (address-feedback).
pr_comment() {
  gh pr comment "$PR_NUMBER" -R "$ORG_REPO" "$@" >/dev/null
}

if [ "$WORKFLOW_NAME" = "address-feedback" ]; then
  PR_NUMBER="$ISSUE_NUMBER"
  # The reviewed PR's repository, as reported by the review event.
  ORG_REPO="$REPO_OWNER/$REPO_NAME"
  echo "$SCRIPT_NAME][INFO] Fetching $ORG_REPO#$PR_NUMBER..."
  PR_JSON=$(gh pr view "$PR_NUMBER" -R "$ORG_REPO" \
    --json state,url,author,baseRefName,headRefName,headRefOid,headRepository,headRepositoryOwner)
  PR_STATE=$(jq -r .state <<<"$PR_JSON")
  PR_URL=$(jq -r .url <<<"$PR_JSON")
  PR_AUTHOR=$(jq -r .author.login <<<"$PR_JSON")
  BASE_REF=$(jq -r .baseRefName <<<"$PR_JSON")
  HEAD_REF=$(jq -r .headRefName <<<"$PR_JSON")
  HEAD_SHA=$(jq -r .headRefOid <<<"$PR_JSON")
  HEAD_REPO=$(jq -r .headRepository.name <<<"$PR_JSON")
  HEAD_OWNER=$(jq -r .headRepositoryOwner.login <<<"$PR_JSON")

  if [ "$PR_STATE" != "OPEN" ]; then
    echo "$SCRIPT_NAME][INFO] $PR_URL is $PR_STATE; nothing to do"
    exit 0
  fi
  # The job pushes to the PR's head branch, so the head repository must be
  # the PR author's fork and this job's token must belong to that author.
  TOKEN_USER=$(gh api user --jq .login)
  if [ "${HEAD_OWNER,,}" != "${PR_AUTHOR,,}" ] || [ "${TOKEN_USER,,}" != "${PR_AUTHOR,,}" ]; then
    echo "$SCRIPT_NAME][ERROR] head repository owner ($HEAD_OWNER), PR author ($PR_AUTHOR) and token user ($TOKEN_USER) must match" >&2
    exit 1
  fi
  if [ "$HEAD_REF" != "$LOCAL_GIT_BRANCH" ]; then
    echo "$SCRIPT_NAME][ERROR] head branch $HEAD_REF does not match $LOCAL_GIT_BRANCH" >&2
    exit 1
  fi
  if [ "$HEAD_SHA" != "$REVIEW_SHA" ]; then
    echo "$SCRIPT_NAME][INFO] PR head moved from $REVIEW_SHA to $HEAD_SHA after the review; not addressing it"
    pr_comment -b "This pull request changed after the review (reviewed \`${REVIEW_SHA:0:7}\`, head is now \`${HEAD_SHA:0:7}\`), so the agent did not address it. Submit a new review on the latest commit to have the feedback addressed."
    exit 0
  fi

  echo "$SCRIPT_NAME][INFO] cloning $HEAD_OWNER/$HEAD_REPO at $HEAD_REF..."
  gh repo clone "$HEAD_OWNER/$HEAD_REPO" "$SERVICE_REPO_DIR" -- --quiet --branch "$HEAD_REF"
  cd "$SERVICE_REPO_DIR"
  if [ "$(git rev-parse HEAD)" != "$HEAD_SHA" ]; then
    echo "$SCRIPT_NAME][ERROR] cloned $HEAD_REF is not at $HEAD_SHA" >&2
    exit 1
  fi
  # The merge base with the PR's base branch, so `git diff PR_BASE_SHA..HEAD`
  # shows only this PR's changes.
  git fetch --quiet "https://github.com/$ORG_REPO.git" "$BASE_REF"
  PR_BASE_SHA=$(git merge-base HEAD FETCH_HEAD)

  # Collect the feedback here, not in the agents, so the agents never call
  # GitHub and only allowed authors' text reaches them.
  FEEDBACK_FILE="${FEEDBACK_FILE:-/tmp/ack-feedback.md}"
  # Review threads the job may reply in, chosen here before the agents run.
  FEEDBACK_TARGETS_FILE="/tmp/ack-feedback-targets.json"
  FEEDBACK_COUNT=$(python "$WORKFLOW_DIR/workflows/feedback.py" \
    --owner "$REPO_OWNER" --repo "$REPO_NAME" --number "$PR_NUMBER" \
    --review-id "$REVIEW_ID" --team-members "$TEAM_MEMBERS" \
    --pr-author "$PR_AUTHOR" --output "$FEEDBACK_FILE" \
    --targets-output "$FEEDBACK_TARGETS_FILE")
  echo "$SCRIPT_NAME][INFO] collected $FEEDBACK_COUNT feedback item(s) into $FEEDBACK_FILE"
  if [ "$FEEDBACK_COUNT" = "0" ]; then
    echo "$SCRIPT_NAME][INFO] no unresolved feedback from team members; nothing to do"
    pr_comment -b "The agent found no unresolved feedback from team members to address."
    exit 0
  fi
  # The harness writes the reply writer's per-item replies here.
  FEEDBACK_REPLIES_FILE="/tmp/ack-feedback-replies.json"
  export PR_URL PR_BASE_SHA FEEDBACK_FILE FEEDBACK_REPLIES_FILE
else
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
fi

cd "$WORKFLOW_DIR"

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
WORKFLOW_ARGS=("$WORKFLOW_COMMAND" --service "$SERVICE" --resource "$RESOURCE")
[ -n "$FIELD" ] && WORKFLOW_ARGS+=(--field "$FIELD")
[ -n "$MODEL" ] && WORKFLOW_ARGS+=(--model "$MODEL")
[ -n "$AWS_SDK_VERSION" ] && WORKFLOW_ARGS+=(--aws-sdk-version "$AWS_SDK_VERSION")

if [ "$WORKFLOW_NAME" = "address-feedback" ]; then
  if ! python -m workflows "${WORKFLOW_ARGS[@]}"; then
    pr_comment -b "The agent could not address this feedback: the workflow failed. See job \`${PROW_JOB_ID:-unknown}\` for details."
    exit 1
  fi
  echo "$SCRIPT_NAME][INFO] $WORKFLOW_LABEL workflow completed successfully"

  cd "$SERVICE_REPO_DIR"
  # The agents are told to leave their changes uncommitted. If one committed
  # or amended anyway, HEAD is no longer the PR head: an amended HEAD cannot
  # be pushed without --force, and the check below would see a clean tree.
  # A soft reset keeps every change, staged, on top of the PR head.
  if [ "$(git rev-parse HEAD)" != "$HEAD_SHA" ]; then
    echo "$SCRIPT_NAME][INFO] the workflow moved HEAD off ${HEAD_SHA:0:7}; folding its commits into one new commit"
    git reset --soft "$HEAD_SHA"
  fi
  # Same rule as roles/e2e.py controller_has_changes: regeneration rewrites
  # ack-generate-metadata.yaml and pkg/version/version.go with code-generator
  # build info on every run, so on their own they are not a change. The
  # pattern must match REGEN_ONLY_PATHS there. Porcelain lines are "XY path";
  # cut drops the status columns. Captured rather than piped into `grep -q`,
  # which can exit early and fail the pipeline under pipefail.
  CHANGES=$(git status --porcelain | cut -c4- \
    | grep -vE '^(apis/[^/]+/ack-generate-metadata\.yaml|pkg/version/version\.go)$' || true)
  if [ -n "$CHANGES" ]; then
    echo "$SCRIPT_NAME][INFO] Committing changes..."
    git add -A >/dev/null
    git commit -m "$COMMIT_MSG" >/dev/null
    # No --force: if the branch moved during the run, the push fails instead
    # of overwriting someone else's commits.
    echo "$SCRIPT_NAME][INFO] Pushing to $HEAD_OWNER/$HEAD_REPO:$HEAD_REF..."
    if ! git push "https://$GITHUB_TOKEN@github.com/$HEAD_OWNER/$HEAD_REPO.git" \
         "HEAD:refs/heads/$HEAD_REF" &>/dev/null; then
      pr_comment -b "The agent addressed this feedback but could not push: \`$HEAD_REF\` changed during the run. Submit a new review to try again."
      exit 1
    fi
    PUSHED_SHA=$(git rev-parse HEAD)
    STATUS_LINE="Pushed $PUSHED_SHA to address this feedback."
  else
    STATUS_LINE="No code changes were needed for this feedback."
  fi

  # Reply in the review threads this run addressed. Reached only after a
  # successful push or a run with no code change, so a reply never describes
  # a commit that did not land. reply.py answers only threads feedback.py
  # listed, and a failed reply does not fail the job.
  if [ -s "$FEEDBACK_REPLIES_FILE" ] && [ -s "$FEEDBACK_TARGETS_FILE" ]; then
    THREAD_REPLIES=$(python "$WORKFLOW_DIR/workflows/reply.py" \
      --targets "$FEEDBACK_TARGETS_FILE" --replies "$FEEDBACK_REPLIES_FILE") || THREAD_REPLIES=0
    echo "$SCRIPT_NAME][INFO] Posted $THREAD_REPLIES review thread reply(ies)"
  fi

  # The marker tells workflows/feedback.py where the next run's conversation
  # feedback starts. Keep it in sync with REPLY_MARKER there.
  REPLY_FILE="/tmp/ack-feedback-reply.md"
  {
    echo "$STATUS_LINE"
    echo
    if [ -s "$PR_BODY_FILE" ]; then
      cat "$PR_BODY_FILE"
    else
      echo "The agent addressed the review feedback."
    fi
    echo
    echo "<!-- ack-agent:feedback-reply -->"
  } > "$REPLY_FILE"
  pr_comment -F "$REPLY_FILE"
  echo "$SCRIPT_NAME][INFO] Posted the reply on $PR_URL"
  exit 0
fi

python -m workflows "${WORKFLOW_ARGS[@]}"
echo "$SCRIPT_NAME][INFO] $WORKFLOW_LABEL workflow completed successfully"

cd "$SERVICE_REPO_DIR"

# Create a new branch
echo "$SCRIPT_NAME][INFO] Creating a new branch..."
git checkout -b "$LOCAL_GIT_BRANCH" >/dev/null

# Commit changes
echo "$SCRIPT_NAME][INFO] Committing changes..."
git add -A  >/dev/null
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
  if [ -n "$FIELD" ]; then
    FALLBACK_BODY="ACK Agent changes adding $FIELD to $RESOURCE in $SERVICE-controller"
  else
    FALLBACK_BODY="ACK Agent changes adding $RESOURCE to $SERVICE-controller"
  fi
  PR_BODY_ARGS=(-b "$FALLBACK_BODY")
fi
if ! gh pr create -R "$ORG_REPO" -t "$COMMIT_MSG" "${PR_BODY_ARGS[@]}" -B "$PR_TARGET_BRANCH" >/dev/null ; then
  echo ""
  echo "gh.sh][ERROR] Failed to create pull request. Exiting... "
  exit 1
fi
echo "ok"

