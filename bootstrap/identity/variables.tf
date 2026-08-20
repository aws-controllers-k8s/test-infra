variable "region" {
  description = "AWS region. The IdC account instance is region-locked and must match the clusters."
  type        = string
  default     = "us-west-2"
}

variable "stage" {
  description = "Deployment stage (dev, staging, prod). Used to name the instance and admin group."
  type        = string
}

variable "argocd_admins" {
  description = <<-EOT
    Users to create in the Identity Store and add to the Argo CD ADMIN group.

    Optional. The capability builds successfully against an empty group, so an empty
    list does not block bootstrap - but nobody can sign in to the Argo CD UI until
    the group has members. Populate this, or federate an external IdP into the
    instance instead.
  EOT

  type = list(object({
    user_name   = string
    given_name  = string
    family_name = string
    email       = string
  }))

  default = []
}
