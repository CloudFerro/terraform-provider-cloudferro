# vim: set softtabstop=2 shiftwidth=2 expandtab :

data "cloudferro_kubernetes_version_v1" "version" {
  version = "1.30.0"
}

data "cloudferro_kubernetes_version_v1" "version_by_id" {
  id = "version id"
}

