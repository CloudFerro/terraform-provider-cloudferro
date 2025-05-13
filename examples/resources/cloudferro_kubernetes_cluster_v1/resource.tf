# vim: set softtabstop=2 shiftwidth=2 expandtab :

resource "cloudferro_kubernetes_cluster_v1" "cluster" {
  control_plane = {
    machine_spec_id = "spec id"
    size            = 1
  }
  name                  = "my cluster"
  kubernetes_version_id = "version id"
}
