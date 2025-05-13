# vim: set softtabstop=2 shiftwidth=2 expandtab :

resource "cloudferro_kubernetes_node_pool_v1" "worker" {
  name            = "worker node pool"
  cluster_id      = "cluster id"
  shared_networks = []
  taints          = []
  labels          = []
  machine_spec_id = "spec id"
}
