resource "cloudferro_kubernetes_cluster_v1" "cluster" {
  control_plane = {
    flavor = "eo2a.2xlarge"
    size   = 5
  }
  name    = "my cluster"
  version = "1.30.10"
}
