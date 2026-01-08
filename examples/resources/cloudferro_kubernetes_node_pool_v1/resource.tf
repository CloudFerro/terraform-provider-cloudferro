resource "cloudferro_kubernetes_node_pool_v1" "worker" {
  name       = "worker node pool"
  cluster_id = "cluster id"
  shared_networks = [
    "shared network id",
  ]
  taints = [
    { key = "key", value = "value", effect = "NoSchedule" },
  ]
  labels = [
    { key = "key", value = "value" },
  ]
  flavor    = "eo2a.2xlarge"
  autoscale = true
  size_min  = 1
  size_max  = 10
}
