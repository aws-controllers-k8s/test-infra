################################################################################
# Public ECR Repository for Prow images
# Public ECR is global but managed from us-east-1.
################################################################################

resource "aws_ecrpublic_repository" "prow_images" {
  provider = aws.us_east_1

  repository_name = local.prow_images_repo

  force_destroy = true
}

################################################################################
# Prow Images Bootstrap
# Builds and pushes the builder image to public ECR on first apply.
# The in-cluster build job handles building all other images.
################################################################################

resource "null_resource" "bootstrap_prow_images" {
  # Only re-run if these change
  triggers = {
    repository_uri = aws_ecrpublic_repository.prow_images.repository_uri
    account_id     = local.account_id
  }

  provisioner "local-exec" {
    command     = "${path.module}/../prow/jobs/images/bootstrap-images.sh"
    environment = {
      AWS_ACCOUNT_ID      = local.account_id
      AWS_REGION          = var.region
      PROW_IMAGE_REPO_URI = aws_ecrpublic_repository.prow_images.repository_uri
    }
  }

  depends_on = [aws_ecrpublic_repository.prow_images]
}
