cluster_name    = "ack-test-infra"
cluster_version = "1.35"
region          = "us-west-2"

vpc_cidr           = "10.0.0.0/16"
availability_zones = ["us-west-2a", "us-west-2b"]

# Flux - must match vendored chart in charts/flux2-<version>/
flux_version = "2.18.3"

git_repository_url    = "https://github.com/aws-controllers-k8s/test-infra"
git_repository_branch = "main"
flux_path             = "./flux"

tags = {
  managed-by  = "terraform-bootstrap"
  environment = "production"
  project     = "ack-test-infra"
}
