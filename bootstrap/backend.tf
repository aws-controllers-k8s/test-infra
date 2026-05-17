terraform {
  backend "s3" {
    bucket       = "ack-test-infra-terraform-state-c44996c1"
    key          = "bootstrap/terraform.tfstate"
    region       = "us-west-2"
    use_lockfile = true
    encrypt      = true
  }
}
