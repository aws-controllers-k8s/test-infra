################################################################################
# VPC
################################################################################

module "vpc" {
  count = var.vpc_id == "" ? 1 : 0

  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.0"

  name = "${var.cluster_name}-vpc"
  cidr = var.vpc_cidr

  azs             = var.availability_zones
  private_subnets = [for i, az in var.availability_zones : cidrsubnet(var.vpc_cidr, 4, i)]
  public_subnets  = [for i, az in var.availability_zones : cidrsubnet(var.vpc_cidr, 4, i + 4)]

  enable_nat_gateway   = true
  single_nat_gateway   = true
  enable_dns_hostnames = true
  enable_dns_support   = true

  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
  }

  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1"
  }
}

################################################################################
# EKS Cluster
################################################################################

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"

  cluster_name    = var.cluster_name
  cluster_version = var.cluster_version

  vpc_id     = local.vpc_id
  subnet_ids = local.subnet_ids

  cluster_compute_config = {
    enabled    = true
    node_pools = ["general-purpose"]
  }

  cluster_endpoint_public_access  = true
  cluster_endpoint_private_access = true

  authentication_mode                      = "API"
  enable_cluster_creator_admin_permissions = true
}

################################################################################
# Node Pool Role - ECR Pull-Through Cache permissions
################################################################################

resource "aws_iam_role_policy" "node_pool_ecr_ptc" {
  name = "ECRPullThroughCache"
  role = module.eks.node_iam_role_name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "ecr:CreateRepository",
        "ecr:BatchImportUpstreamImage"
      ]
      Resource = "arn:${local.partition}:ecr:${var.region}:${local.account_id}:repository/fluxcd/*"
    }]
  })
}

################################################################################
# Cluster Security Group - Webhook ingress rule
################################################################################

resource "aws_security_group" "prow_webhook_nlb" {
  name        = "prow-webhook-nlb-sg"
  description = "Security group for Prow webhook NLB (GitHub IPs only)"
  vpc_id      = local.vpc_id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
    description = "Allow all outbound"
  }

  ingress {
    from_port   = 8888
    to_port     = 8888
    protocol    = "tcp"
    cidr_blocks = ["192.30.252.0/22", "185.199.108.0/22", "140.82.112.0/20"]
    description = "GitHub webhook"
  }

  ingress {
    from_port   = 8888
    to_port     = 8888
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
    description = "NLB health checks"
  }
}

resource "aws_vpc_security_group_ingress_rule" "webhook_to_cluster" {
  security_group_id            = module.eks.cluster_primary_security_group_id
  referenced_security_group_id = aws_security_group.prow_webhook_nlb.id
  from_port                    = 8888
  to_port                      = 8888
  ip_protocol                  = "tcp"
  description                  = "Webhook NLB to Prow hook pods"
}

################################################################################
# Cluster Admin Role
################################################################################

resource "aws_iam_role" "cluster_admin" {
  name = "ack-cluster-admin-access-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "cluster_admin_describe" {
  name = "DescribeCluster"
  role = aws_iam_role.cluster_admin.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["eks:DescribeCluster"]
      Resource = module.eks.cluster_arn
    }]
  })
}

################################################################################
# Providers (Kubernetes + Helm + kubectl)
################################################################################

provider "kubernetes" {
  host                   = module.eks.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
  }
}

provider "helm" {
  kubernetes = {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)

    exec = {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
    }
  }
}

provider "kubectl" {
  host                   = module.eks.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)
  load_config_file       = false

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
  }
}
