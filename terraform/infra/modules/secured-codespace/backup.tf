# ---------------------------------------------------------------------------
# AWS Backup for per-workspace EBS volumes (defense-in-depth on top of the EBS
# durability). Selects volumes by the "backup=secured-workspace" tag the EBS CSI
# driver stamps at create time (tagSpecification in internal/hashistack/nomad.go).
# A daily snapshot with 30-day retention. Toggle with var.enable_workspace_backups.
# ---------------------------------------------------------------------------

resource "aws_backup_vault" "workspace" {
  count = var.enable_workspace_backups ? 1 : 0
  name  = "${local.name}-workspace"
  tags  = { owner = var.owner }
}

resource "aws_backup_plan" "workspace" {
  count = var.enable_workspace_backups ? 1 : 0
  name  = "${local.name}-workspace"

  rule {
    rule_name         = "daily"
    target_vault_name = aws_backup_vault.workspace[0].name
    schedule          = "cron(0 3 * * ? *)" # 03:00 UTC daily
    start_window      = 60
    completion_window = 180

    lifecycle {
      delete_after = 30 # days
    }
  }

  tags = { owner = var.owner }
}

data "aws_iam_policy_document" "backup_assume" {
  count = var.enable_workspace_backups ? 1 : 0
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["backup.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "backup" {
  count              = var.enable_workspace_backups ? 1 : 0
  name               = "${local.name}-backup"
  assume_role_policy = data.aws_iam_policy_document.backup_assume[0].json
  tags               = { owner = var.owner }
}

resource "aws_iam_role_policy_attachment" "backup" {
  count      = var.enable_workspace_backups ? 1 : 0
  role       = aws_iam_role.backup[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSBackupServiceRolePolicyForBackup"
}

resource "aws_backup_selection" "workspace" {
  count        = var.enable_workspace_backups ? 1 : 0
  name         = "${local.name}-workspace-volumes"
  plan_id      = aws_backup_plan.workspace[0].id
  iam_role_arn = aws_iam_role.backup[0].arn

  selection_tag {
    type  = "STRINGEQUALS"
    key   = "backup"
    value = "secured-workspace"
  }
}
