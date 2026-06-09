# Public Network Load Balancer fronting the Boundary controller API (9200) and the
# worker proxy (9202). TCP pass-through: Boundary terminates its own TLS end-to-end.
resource "aws_lb" "this" {
  name               = "${local.name}-nlb"
  load_balancer_type = "network"
  internal           = false
  subnets            = aws_subnet.public[*].id
  security_groups    = [aws_security_group.nlb.id]

  tags = {
    Name  = "${local.name}-nlb"
    owner = var.owner
  }
}

resource "aws_lb_target_group" "api" {
  name        = "${local.name}-api"
  port        = 9200
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "instance"

  health_check {
    protocol = "TCP"
    port     = "9200"
  }

  tags = {
    Name  = "${local.name}-api"
    owner = var.owner
  }
}

resource "aws_lb_target_group" "proxy" {
  name        = "${local.name}-proxy"
  port        = 9202
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "instance"

  health_check {
    protocol = "TCP"
    port     = "9202"
  }

  tags = {
    Name  = "${local.name}-proxy"
    owner = var.owner
  }
}

resource "aws_lb_target_group" "nomad" {
  name        = "${local.name}-nomad"
  port        = 4646
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "instance"

  health_check {
    protocol = "TCP"
    port     = "4646"
  }

  tags = {
    Name  = "${local.name}-nomad"
    owner = var.owner
  }
}

resource "aws_lb_target_group" "vault" {
  name        = "${local.name}-vault"
  port        = 8200
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "instance"

  health_check {
    protocol = "TCP"
    port     = "8200"
  }

  tags = {
    Name  = "${local.name}-vault"
    owner = var.owner
  }
}

resource "aws_lb_target_group" "mcp" {
  name        = "${local.name}-mcp"
  port        = 4444
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "instance"

  health_check {
    protocol = "TCP"
    port     = "4444"
  }

  tags = {
    Name  = "${local.name}-mcp"
    owner = var.owner
  }
}

resource "aws_lb_target_group" "llm" {
  name        = "${local.name}-llm"
  port        = 4000
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "instance"

  health_check {
    protocol = "TCP"
    port     = "4000"
  }

  tags = {
    Name  = "${local.name}-llm"
    owner = var.owner
  }
}

resource "aws_lb_target_group" "portal" {
  name        = "${local.name}-portal"
  port        = 8443
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "instance"

  health_check {
    protocol = "TCP"
    port     = "8443"
  }

  tags = {
    Name  = "${local.name}-portal"
    owner = var.owner
  }
}

resource "aws_lb_target_group_attachment" "api" {
  target_group_arn = aws_lb_target_group.api.arn
  target_id        = aws_instance.this.id
  port             = 9200
}

resource "aws_lb_target_group_attachment" "proxy" {
  target_group_arn = aws_lb_target_group.proxy.arn
  target_id        = aws_instance.this.id
  port             = 9202
}

resource "aws_lb_target_group_attachment" "nomad" {
  target_group_arn = aws_lb_target_group.nomad.arn
  target_id        = aws_instance.this.id
  port             = 4646
}

resource "aws_lb_target_group_attachment" "vault" {
  target_group_arn = aws_lb_target_group.vault.arn
  target_id        = aws_instance.this.id
  port             = 8200
}

resource "aws_lb_target_group_attachment" "mcp" {
  target_group_arn = aws_lb_target_group.mcp.arn
  target_id        = aws_instance.this.id
  port             = 4444
}

resource "aws_lb_target_group_attachment" "llm" {
  target_group_arn = aws_lb_target_group.llm.arn
  target_id        = aws_instance.this.id
  port             = 4000
}

resource "aws_lb_target_group_attachment" "portal" {
  target_group_arn = aws_lb_target_group.portal.arn
  target_id        = aws_instance.this.id
  port             = 8443
}

resource "aws_lb_listener" "api" {
  load_balancer_arn = aws_lb.this.arn
  port              = 9200
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
}

resource "aws_lb_listener" "proxy" {
  load_balancer_arn = aws_lb.this.arn
  port              = 9202
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.proxy.arn
  }
}

resource "aws_lb_listener" "nomad" {
  load_balancer_arn = aws_lb.this.arn
  port              = 4646
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.nomad.arn
  }
}

resource "aws_lb_listener" "vault" {
  load_balancer_arn = aws_lb.this.arn
  port              = 8200
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.vault.arn
  }
}

# ContextForge MCP Gateway admin API (plaintext HTTP — the gateway terminates no
# TLS; the listener ingress is locked to the operator /32 in network.tf).
resource "aws_lb_listener" "mcp" {
  load_balancer_arn = aws_lb.this.arn
  port              = 4444
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.mcp.arn
  }
}

# LiteLLM AI Gateway (plaintext HTTP — the gateway terminates no TLS; the listener
# ingress is locked to the operator /32 in network.tf, used only for admin key
# provisioning. Workspaces reach the gateway node-private, not via the NLB).
resource "aws_lb_listener" "llm" {
  load_balancer_arn = aws_lb.this.arn
  port              = 4000
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.llm.arn
  }
}

# Developer Portal (HTTPS — the portal terminates its own TLS on :8443; the
# listener ingress is locked to the operator /32 in network.tf).
resource "aws_lb_listener" "portal" {
  load_balancer_arn = aws_lb.this.arn
  port              = 8443
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.portal.arn
  }
}
