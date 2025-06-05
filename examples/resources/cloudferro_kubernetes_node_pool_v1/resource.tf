resource "cloudferro_kubernetes_node_pool_v1" "worker" {
  name            = "worker node pool"
  cluster_id      = "cluster id"
  shared_networks = []
  taints          = []
  labels          = []
  flavor          = "eo2a.2xlarge"
}
