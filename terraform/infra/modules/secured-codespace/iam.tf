# ---------------------------------------------------------------------------
# EC2 instance IAM — added so the AWS EBS CSI driver (ebs-csi.tf) can manage
# per-workspace EBS volumes. Before this the instances had NO instance profile
# (the stack never called the AWS API). The role carries a minimal EBS-CSI policy
# and is attached to all four instance types (all-in-one, gpu, microvm, agent).
#
# NOTE: aws_instance.this has lifecycle ignore_changes=all, so attaching this
# profile takes effect only on a fresh launch (the intended destroy+recreate
# rollout), not via terraform apply on an already-running node.
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "instance_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "instance" {
  name               = "${local.name}-node"
  assume_role_policy = data.aws_iam_policy_document.instance_assume.json
  tags               = { owner = var.owner }
}

# Minimal EBS CSI controller permissions — a scoped subset of the upstream
# AmazonEBSCSIDriverPolicy. CreateTags is constrained to the create actions.
data "aws_iam_policy_document" "ebs_csi" {
  statement {
    sid    = "EBSVolumeLifecycle"
    effect = "Allow"
    actions = [
      "ec2:CreateVolume",
      "ec2:DeleteVolume",
      "ec2:AttachVolume",
      "ec2:DetachVolume",
      "ec2:ModifyVolume",
      "ec2:DescribeVolumes",
      "ec2:DescribeVolumesModifications",
      "ec2:DescribeInstances",
      "ec2:DescribeAvailabilityZones",
      "ec2:DescribeTags",
      "ec2:DescribeSnapshots",
      "ec2:CreateSnapshot",
      "ec2:DeleteSnapshot",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "EBSCreateTags"
    effect    = "Allow"
    actions   = ["ec2:CreateTags"]
    resources = ["arn:aws:ec2:*:*:volume/*", "arn:aws:ec2:*:*:snapshot/*"]
    condition {
      test     = "StringEquals"
      variable = "ec2:CreateAction"
      values   = ["CreateVolume", "CreateSnapshot"]
    }
  }
}

resource "aws_iam_role_policy" "ebs_csi" {
  name   = "ebs-csi"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.ebs_csi.json
}

# EFS access-point lifecycle for the EFS CSI driver (efs-csi.tf) — dynamic
# provisioning creates/deletes one access point per shared volume. Only attached
# when the shared-volume feature is on, so the flag-off deployment is unchanged.
data "aws_iam_policy_document" "efs_csi" {
  statement {
    sid    = "EFSAccessPointLifecycle"
    effect = "Allow"
    actions = [
      "elasticfilesystem:DescribeAccessPoints",
      "elasticfilesystem:DescribeFileSystems",
      "elasticfilesystem:DescribeMountTargets",
      "elasticfilesystem:CreateAccessPoint",
      "elasticfilesystem:DeleteAccessPoint",
      "elasticfilesystem:TagResource",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "efs_csi" {
  count  = var.enable_shared_volume ? 1 : 0
  name   = "efs-csi"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.efs_csi.json
}

resource "aws_iam_instance_profile" "instance" {
  name = "${local.name}-node"
  role = aws_iam_role.instance.name
}
