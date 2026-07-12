locals {
  name             = "${var.owner}-boundary"
  allowed_cidr     = "${chomp(data.http.my_public_ip.response_body)}/32"
  ssh_key_filename = "${var.owner}-boundary-ssh_key"
}

# Caller's public IP — the sole source CIDR allowed onto every exposed port
# (the NLB listeners and SSH).
data "http" "my_public_ip" {
  url = "https://checkip.amazonaws.com"
}

# AMI built by ami/base_image (Boundary/Consul/Nomad/Vault enterprise binaries baked in).
data "aws_ami" "this" {
  most_recent = true
  owners      = ["self"]

  filter {
    name   = "name"
    values = ["${var.owner}-boundary-enterprise-*"]
  }
}

data "aws_availability_zones" "available" {
  state = "available"
}

# =====================
# Dedicated VPC + public networking (two AZs for the NLB)
# =====================

resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = {
    Name  = local.name
    owner = var.owner
  }
}

resource "aws_subnet" "public" {
  count = length(var.public_subnet_cidrs)

  vpc_id                  = aws_vpc.main.id
  cidr_block              = var.public_subnet_cidrs[count.index]
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  map_public_ip_on_launch = true

  tags = {
    Name  = "${local.name}-public-${count.index}"
    owner = var.owner
  }
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id

  tags = {
    Name  = local.name
    owner = var.owner
  }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }

  tags = {
    Name  = "${local.name}-public"
    owner = var.owner
  }
}

resource "aws_route_table_association" "public" {
  count = length(aws_subnet.public)

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# =====================
# EC2 key pair (private key written to ./generated for SSH/scp)
# =====================

resource "tls_private_key" "ssh" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "aws_key_pair" "this" {
  key_name   = "${local.name}-key"
  public_key = tls_private_key.ssh.public_key_openssh
}

resource "local_file" "private_key" {
  content         = tls_private_key.ssh.private_key_openssh
  filename        = "${path.root}/generated/${local.ssh_key_filename}"
  file_permission = "0400"
}

# =====================
# Security groups — every exposed port is locked to the caller's /32
# =====================

# Public NLB: Boundary API (9200) and worker proxy (9202).
resource "aws_security_group" "nlb" {
  name        = "${local.name}-nlb"
  description = "NLB ingress for Boundary API and worker proxy"
  vpc_id      = aws_vpc.main.id

  ingress {
    description = "Boundary API HTTPS"
    from_port   = 9200
    to_port     = 9200
    protocol    = "tcp"
    cidr_blocks = [local.allowed_cidr]
  }

  ingress {
    description = "Boundary worker proxy"
    from_port   = 9202
    to_port     = 9202
    protocol    = "tcp"
    cidr_blocks = [local.allowed_cidr]
  }

  ingress {
    description = "Nomad HTTP API/UI"
    from_port   = 4646
    to_port     = 4646
    protocol    = "tcp"
    cidr_blocks = [local.allowed_cidr]
  }

  ingress {
    description = "Vault API/UI"
    from_port   = 8200
    to_port     = 8200
    protocol    = "tcp"
    cidr_blocks = [local.allowed_cidr]
  }

  ingress {
    description = "ContextForge MCP Gateway admin API"
    from_port   = 4444
    to_port     = 4444
    protocol    = "tcp"
    cidr_blocks = [local.allowed_cidr]
  }

  ingress {
    description = "LiteLLM AI Gateway admin API (virtual-key provisioning)"
    from_port   = 4000
    to_port     = 4000
    protocol    = "tcp"
    cidr_blocks = [local.allowed_cidr]
  }

  ingress {
    description = "Developer Portal HTTPS"
    from_port   = 8443
    to_port     = 8443
    protocol    = "tcp"
    cidr_blocks = [local.allowed_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name  = "${local.name}-nlb"
    owner = var.owner
  }
}

# All-in-one instance: SSH from the caller (for Terraform provisioners), and the
# Boundary ports only from the NLB.
resource "aws_security_group" "instance" {
  name        = "${local.name}-instance"
  description = "All-in-one Boundary node"
  vpc_id      = aws_vpc.main.id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [local.allowed_cidr]
  }

  ingress {
    description     = "Boundary API HTTPS from NLB"
    from_port       = 9200
    to_port         = 9200
    protocol        = "tcp"
    security_groups = [aws_security_group.nlb.id]
  }

  ingress {
    description     = "Boundary worker proxy from NLB"
    from_port       = 9202
    to_port         = 9202
    protocol        = "tcp"
    security_groups = [aws_security_group.nlb.id]
  }

  ingress {
    description     = "Nomad HTTP API/UI from NLB"
    from_port       = 4646
    to_port         = 4646
    protocol        = "tcp"
    security_groups = [aws_security_group.nlb.id]
  }

  ingress {
    description     = "Vault API/UI from NLB"
    from_port       = 8200
    to_port         = 8200
    protocol        = "tcp"
    security_groups = [aws_security_group.nlb.id]
  }

  ingress {
    description     = "MCP gateway admin API from NLB"
    from_port       = 4444
    to_port         = 4444
    protocol        = "tcp"
    security_groups = [aws_security_group.nlb.id]
  }

  ingress {
    description     = "LiteLLM AI gateway from NLB (admin key provisioning)"
    from_port       = 4000
    to_port         = 4000
    protocol        = "tcp"
    security_groups = [aws_security_group.nlb.id]
  }

  ingress {
    description     = "Developer Portal HTTPS from NLB"
    from_port       = 8443
    to_port         = 8443
    protocol        = "tcp"
    security_groups = [aws_security_group.nlb.id]
  }

  # --- Intra-SG rules so worker nodes (same SG) can reach the main node ---
  # On the all-in-one node every component is co-located, so dev-workspace talks to
  # Vault/demo-db over loopback and the Boundary worker dials workspaces on-host —
  # no SG rule was needed. The GPU and microVM nodes are separate EC2s in this same
  # SG, so that traffic now crosses the network. self=true scopes these to members
  # of this SG (only the main + GPU + microVM nodes), nothing wider.
  ingress {
    description = "Nomad RPC + serf between server and GPU/microVM clients"
    from_port   = 4647
    to_port     = 4648
    protocol    = "tcp"
    self        = true
  }

  ingress {
    description = "Vault API from the GPU/microVM clients (Nomad-Vault WIF + template secrets)"
    from_port   = 8200
    to_port     = 8200
    protocol    = "tcp"
    self        = true
  }

  ingress {
    description = "demo-db Postgres from GPU/microVM workspaces"
    from_port   = 15432
    to_port     = 15432
    protocol    = "tcp"
    self        = true
  }

  ingress {
    description = "Boundary worker (main node) to GPU/microVM workspace SSH (portgen range)"
    from_port   = 2222
    to_port     = 2399
    protocol    = "tcp"
    self        = true
  }

  ingress {
    description = "MCP gateway (main node) reached by GPU/microVM workspaces (remote Claude MCP)"
    from_port   = 4444
    to_port     = 4444
    protocol    = "tcp"
    self        = true
  }

  ingress {
    description = "LiteLLM gateway (main node) reached by GPU/microVM workspaces (Claude Code LLM traffic)"
    from_port   = 4000
    to_port     = 4000
    protocol    = "tcp"
    self        = true
  }

  # Project MCP servers run as Nomad jobs on the "agents" node pool and the
  # ContextForge gateway (main node) federates to them at nodeIP:<port> to run the
  # consumption-mirror test + serve workspaces. Since Phase F the portal maps them
  # to Nomad DYNAMIC host ports (default range 20000-32000; the old 8080-8099
  # static band retired with the platform-admin MCP plane), so open that range
  # intra-SG (self=true confines it to cluster members). Without this the
  # gateway's dial is dropped → its POST /gateways peer validation hangs →
  # portal Test times out with "upstream service error".
  ingress {
    description = "MCP servers on agent nodes (Nomad dynamic ports) reached by the ContextForge gateway (main node)"
    from_port   = 20000
    to_port     = 32000
    protocol    = "tcp"
    self        = true
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name  = "${local.name}-instance"
    owner = var.owner
  }
}
