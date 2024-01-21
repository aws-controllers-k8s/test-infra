#!/usr/bin/env bash

set -eo pipefail

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

ensure_repository_catalog() {
    local ecr_repo=$1
    local ecr_tpl_file_path=""
    local aws_service=""

    echo "public-ecr-set-catalog.sh][INFO] setting catalog data for $ecr_repo public repository ..."

    if (echo "$ecr_repo" | grep -q "controller"); then
        aws_service="${ecr_repo/%-controller}"
        ecr_tpl_file_path="$THIS_DIR/ecr-templates/ecr-controller-template.json"
    else
        aws_service="${ecr_repo/%-chart}"
        ecr_tpl_file_path="$THIS_DIR/ecr-templates/ecr-chart-template.json"
    fi

    export aws_service
    local catalog_data=$(envsubst < $ecr_tpl_file_path 2>&1)

    aws ecr-public put-repository-catalog-data --region us-east-1 --repository-name $ecr_repo --catalog-data "$catalog_data" 1>/dev/null
}

REPOS=$(aws ecr-public describe-repositories --region us-east-1 --query 'repositories[].repositoryName' --output text)

for ecr_repo in $REPOS; do
    ensure_repository_catalog $ecr_repo
done