terraform {
  backend "s3" {
    bucket       = "ack-test-infra-terraform-state"
    key          = "bootstrap/terraform.tfstate"
    region       = "us-west-2"
    use_lockfile = true
    encrypt      = true
  }
}
