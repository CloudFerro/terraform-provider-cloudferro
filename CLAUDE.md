# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Terraform provider for provisioning resources on CloudFerro clouds. Built on the
HashiCorp `terraform-plugin-framework` (not the legacy SDKv2). The provider talks to the
CloudFerro backends either via gRPC or REST.

## Commands

```sh
make build        # fmtcheck + go install
make test         # unit tests (go test, parallel)
make testacc      # acceptance tests — sets TF_ACC=1, hits a real backend, 120m timeout
make lint         # golangci-lint run
make fmt          # gofmt -w
make generate     # go generate . — regenerates docs/ via tfplugindocs (run after schema changes)
make dev-install  # build + install locally into the /tmp/tf-workaround registry mirror for manual testing
```

Run a single test: `go test ./cloudferro/ -run TestName -v`.

Linting uses `gofumpt` formatting plus `gocritic`, `gosec`, `revive`, and `govet` (all analyzers
except `fieldalignment`). Note: there are currently no `_test.go` files in the tree.

## Required environment

The backend API package `gitlab.cloudferro.com/k8s/api` is a **private module**. You must set
`GOPRIVATE=gitlab.cloudferro.com/*` (handled by `.envrc` via direnv) and have git credentials for
that host, or `go mod download` / `go build` will fail. CI authenticates with a `.netrc` job token.

## Architecture

**Entry point** (`main.go`): serves the provider via `providerserver.Serve`. `version` is the
`"dev"` constant locally, overridden by goreleaser at release. `-debug` flag enables debug mode.

**Provider** (`cloudferro/provider.go`): `Configure` builds a single `*grpc.ClientConn` and stores
it in a `providerState{Cli}` that is handed to every resource via `resp.ResourceData`. Resources
retrieve it in their own `Configure` by type-asserting `req.ProviderData.(*providerState)`.
- Connection config: `host` (or `region`, which expands to `managed-kubernetes.<region>.cloudferro.com`
  — the two are mutually exclusive), `token`, `server_cert`. Each falls back to a `CLOUDFERRO_*` env var.
- The client is wired with TLS (system pool + optional extra cert), per-RPC token auth
  (`Authorization: Token <token>`), a retry service config (retries `UNAVAILABLE`, max 10 attempts),
  a User-Agent, and logging interceptors that surface the backend `x-request-id` on failures.

**Resources** (`cloudferro/resource_*_v1.go`): two resources —
`cloudferro_kubernetes_cluster_v1` and `cloudferro_kubernetes_node_pool_v1`. Each instantiates
typed gRPC clients from the api module (e.g. `clusterservice.NewClusterClient(cli)`) per call.
Cross-cutting patterns to preserve:
- **Per-resource locking**: `getResourceMutex(clusterID)` serializes concurrent operations against
  the same cluster (cluster + its node pools share the cluster's mutex).
- **Polling**: create/update/delete issue the gRPC call then `for {}`-poll `GetCluster` until the
  resource reaches the desired/terminal state, mapping backend status into Terraform state.
- **Error enrichment**: `internal/utils.GetLatestClusterError` fetches the most recent backend
  error for a cluster to produce actionable diagnostics (used during async failures).

**internal/kubeconfig** (`parse.go`): parses a cluster's kubeconfig YAML into typed fields
(`host`, `client_certificate`, `client_key`, `cluster_ca_certificate`) exposed on the cluster
resource's `kubeconfig` attribute.

## Docs & examples

`docs/` is **generated** by `tfplugindocs` from resource/attribute `Description` fields and the
`examples/` HCL files — edit schema descriptions and examples, then `make generate`; do not hand-edit
`docs/`. Registry metadata lives in `terraform-registry-manifest.json`.

When reviewing code, check that the `examples/` HCL still matches the resource schemas — that
attribute names, required/optional fields, and types in the examples are correct and reflect any
schema changes in the diff.

