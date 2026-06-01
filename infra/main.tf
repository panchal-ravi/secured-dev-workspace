module "boundary_allinone" {
  source = "./modules/boundary-allinone"

  owner            = var.owner
  region           = var.region
  instance_type    = var.instance_type
  boundary_version = var.boundary_version
  boundary_license = file("${path.root}/config/boundary_license.hclic")

  boundary_admin_login_name = var.boundary_admin_login_name
  boundary_admin_password   = var.boundary_admin_password
  boundary_org_name         = var.boundary_org_name
  boundary_project_name     = var.boundary_project_name
}
