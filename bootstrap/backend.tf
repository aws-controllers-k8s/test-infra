# Configure remote state storage.
# Uncomment and fill in values for your environment.
#
# terraform {
#   backend "s3" {
#     bucket         = "ack-test-infra-terraform-state"
#     key            = "bootstrap/terraform.tfstate"
#     region         = "us-west-2"
#     dynamodb_table = "ack-test-infra-terraform-lock"
#     encrypt        = true
#   }
# }
