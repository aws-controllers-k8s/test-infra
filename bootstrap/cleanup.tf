################################################################################
# Destroy-time Cleanup Operations
#
# These resources handle cleanup of resources that are retained by ACK
# (deletion-policy: retain) or created outside of Terraform's management.
# They run during `terraform destroy` to ensure a clean teardown.
################################################################################

# Strips finalizers from all Flux CRs and namespaces BEFORE Helm uninstalls
# the controllers. Prevents the deadlock where namespaces get stuck in
# Terminating because finalizer-processing controllers are already gone.
resource "null_resource" "flux_suspend" {
  triggers = {
    cluster_name = aws_eks_cluster.this.name
    region       = var.region
  }

  provisioner "local-exec" {
    when    = destroy
    command = <<-EOT
      aws eks update-kubeconfig --name ${self.triggers.cluster_name} --region ${self.triggers.region} 2>/dev/null || true

      # Step 1: Scale down all Flux controllers to prevent finalizer re-addition
      kubectl scale deployment -n flux-system --all --replicas=0 2>/dev/null || true
      sleep 5

      # Step 2: Remove finalizers from ALL Flux custom resources and delete them
      for crd in kustomizations.kustomize.toolkit.fluxcd.io \
                 gitrepositories.source.toolkit.fluxcd.io \
                 helmreleases.helm.toolkit.fluxcd.io \
                 helmrepositories.source.toolkit.fluxcd.io \
                 helmcharts.source.toolkit.fluxcd.io; do
        kubectl get "$crd" -A -o json 2>/dev/null | \
          jq -r '.items[] | "\(.metadata.namespace)/\(.metadata.name)"' 2>/dev/null | \
          xargs -I {} sh -c "
            ns=\$(echo {} | cut -d/ -f1)
            name=\$(echo {} | cut -d/ -f2)
            kubectl patch $crd \$name -n \$ns --type merge -p '{\"metadata\":{\"finalizers\":null}}' 2>/dev/null || true
            kubectl delete $crd \$name -n \$ns --wait=false 2>/dev/null || true
          "
      done

      # Step 3: Wait for CRs to be gone
      sleep 5

      # Step 4: Remove finalizers from namespaces
      for ns in flux-system ack-system; do
        kubectl get ns "$ns" -o json 2>/dev/null | \
          jq '.spec.finalizers = [] | .metadata.finalizers = []' | \
          kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f - 2>/dev/null || true
      done

      echo "Flux pre-destroy cleanup complete"
    EOT
    on_failure = continue
  }
}

# Cleans up the ACK capability role (created by ACK in-cluster, retained
# during destroy via deletion-policy: retain on the Role CR).
resource "null_resource" "cleanup_ack_capability_role" {
  triggers = {
    role_name = "${local.cluster_name}-ack-capability-role"
    region    = var.region
  }

  provisioner "local-exec" {
    when    = destroy
    command = <<-EOT
      echo "Cleaning up ACK capability role: ${self.triggers.role_name}"

      # Delete all inline policies first
      for policy in $(aws iam list-role-policies --role-name ${self.triggers.role_name} --query 'PolicyNames[]' --output text --region ${self.triggers.region} 2>/dev/null); do
        aws iam delete-role-policy --role-name ${self.triggers.role_name} --policy-name "$policy" --region ${self.triggers.region} 2>/dev/null || true
      done

      # Detach all managed policies
      for policy_arn in $(aws iam list-attached-role-policies --role-name ${self.triggers.role_name} --query 'AttachedPolicies[].PolicyArn' --output text --region ${self.triggers.region} 2>/dev/null); do
        aws iam detach-role-policy --role-name ${self.triggers.role_name} --policy-arn "$policy_arn" --region ${self.triggers.region} 2>/dev/null || true
      done

      # Delete the role
      aws iam delete-role --role-name ${self.triggers.role_name} --region ${self.triggers.region} 2>/dev/null || true

      echo "ACK capability role cleanup complete"
    EOT
    on_failure = continue
  }
}

# Empties and deletes the Prow logs S3 bucket (retained by ACK).
resource "null_resource" "cleanup_prow_logs_bucket" {
  triggers = {
    bucket_name = "ack-prow-logs-${local.account_id}"
    region      = var.region
  }

  provisioner "local-exec" {
    when    = destroy
    command = <<-EOT
      echo "Cleaning up Prow logs bucket: ${self.triggers.bucket_name}"

      # Empty the bucket (delete all objects and versions)
      aws s3 rm "s3://${self.triggers.bucket_name}" --recursive --region ${self.triggers.region} 2>/dev/null || true

      # Delete any remaining object versions and delete markers
      aws s3api list-object-versions --bucket ${self.triggers.bucket_name} --region ${self.triggers.region} --query '{Objects: [].{Key:Key,VersionId:VersionId}}' --output json 2>/dev/null | \
        aws s3api delete-objects --bucket ${self.triggers.bucket_name} --region ${self.triggers.region} --delete file:///dev/stdin 2>/dev/null || true

      # Delete the bucket
      aws s3api delete-bucket --bucket ${self.triggers.bucket_name} --region ${self.triggers.region} 2>/dev/null || true

      echo "Prow logs bucket cleanup complete"
    EOT
    on_failure = continue
  }
}
