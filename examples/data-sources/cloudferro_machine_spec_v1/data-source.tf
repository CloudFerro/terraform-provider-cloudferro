# vim: set softtabstop=2 shiftwidth=2 expandtab :

data "cloudferro_machine_spec_v1" "spec" {
  id = "spec id"
}

data "cloudferro_machine_spec_v1" "spec_by_name_and_region" {
  name   = "eo2.xlarge"
  region = "WAW4-1"
}
