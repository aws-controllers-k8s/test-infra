#!/usr/bin/env bash

set -eo pipefail

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

ensure_repository() {
    local aws_service=$1
    local ecr_repos=("$aws_service-controller" "$aws_service-chart")
    local ecr_tpl_file_path=""

    for ecr_repo in "${ecr_repos[@]}"; do
        if (echo "$ecr_repo" | grep -q "controller"); then
            ecr_tpl_file_path="$THIS_DIR/ecr-templates/ecr-controller-template.json"
        else
            ecr_tpl_file_path="$THIS_DIR/ecr-templates/ecr-chart-template.json"
        fi

        export aws_service
        local catalog_data=$(envsubst < $ecr_tpl_file_path 2>&1)

        if ! (aws ecr-public describe-repositories --region us-east-1 --repository-names $ecr_repo >/dev/null 2>&1); then
            echo "ensure-ecr-repository.sh][INFO] $ecr_repo repository does not exist in Amazon ECR public repositories for AWS Controllers for Kubernetes (ACK), creating $ecr_repo public repository ..."
            aws ecr-public create-repository --region us-east-1 --repository-name $ecr_repo 1>/dev/null
            aws ecr-public put-repository-catalog-data --region us-east-1 --repository-name $ecr_repo --catalog-data "$catalog_data" 1>/dev/null
        fi
    done
}
