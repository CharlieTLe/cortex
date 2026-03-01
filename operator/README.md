# Cortex Operator

A Kubernetes operator that automates the deployment and lifecycle management of [Cortex](https://cortexmetrics.io), a horizontally scalable, multi-tenant, long-term storage system for Prometheus.

Instead of manually creating Deployments, StatefulSets, ConfigMaps, and Services for each Cortex component, this operator lets you manage an entire Cortex cluster with a single `Cortex` Custom Resource.

## Components Managed

| Component | Workload | Description |
|-----------|----------|-------------|
| Distributor | Deployment | Receives incoming write requests and forwards them to ingesters |
| Ingester | StatefulSet | Writes incoming series to long-term storage, serves recent data for reads |
| Querier | Deployment | Handles read queries by fetching data from ingesters and long-term storage |
| Query Frontend | Deployment | Provides query splitting, caching, and retries in front of queriers |
| Store Gateway | StatefulSet | Serves blocks from long-term object storage |
| Compactor | StatefulSet | Compacts and deduplicates blocks in long-term storage |

## Prerequisites

- Go 1.22+
- Docker 17.03+
- kubectl v1.24+
- Access to a Kubernetes v1.24+ cluster
- [Kind](https://kind.sigs.k8s.io/) (for local development)

## Quick Start (Kind)

This walks through deploying the operator and a Cortex cluster locally using Kind with MinIO as the S3-compatible storage backend.

### 1. Create a Kind cluster

```sh
kind create cluster --name cortex-test
```

### 2. Deploy everything

A single Makefile target builds the operator image, loads it into Kind, installs CRDs, deploys the operator, sets up MinIO with a pre-created bucket, and applies a sample Cortex CR:

```sh
cd operator/
make kind-setup
```

### 3. Verify the deployment

Check that all pods are running (it may take a few minutes for all Cortex pods to become ready):

```sh
make kind-status
```

Expected resources in the `cortex` namespace:

| Resource | Name | Notes |
|----------|------|-------|
| ConfigMap | `<name>-config` | Generated Cortex YAML configuration |
| Service | `<name>-gossip` | Headless, memberlist gossip (port 7946) |
| Service | `<name>-distributor` | ClusterIP (HTTP 8080, gRPC 9095) |
| Service | `<name>-ingester-headless` | Headless, for StatefulSet pod DNS |
| Service | `<name>-querier` | ClusterIP |
| Service | `<name>-query-frontend` | ClusterIP |
| Service | `<name>-query-frontend-headless` | Headless, for querier DNS discovery |
| Service | `<name>-store-gateway-headless` | Headless |
| Service | `<name>-compactor` | ClusterIP |
| Service | `<name>-compactor-headless` | Headless, for StatefulSet pod DNS |
| Deployment | `<name>-distributor` | Stateless |
| Deployment | `<name>-querier` | Stateless |
| Deployment | `<name>-query-frontend` | Stateless |
| StatefulSet | `<name>-ingester` | With PVC for WAL data |
| StatefulSet | `<name>-store-gateway` | With PVC for cache |
| StatefulSet | `<name>-compactor` | With PVC for working directory |
| PDB | `<name>-ingester` | maxUnavailable: 1 |
| PDB | `<name>-store-gateway` | maxUnavailable: 1 |
| PDB | `<name>-compactor` | maxUnavailable: 1 |

### 4. Test operations

**Scale a component:**

```sh
kubectl -n cortex patch cortex test --type=merge -p '{"spec":{"distributor":{"replicas":3}}}'
kubectl -n cortex get deployment test-distributor -w
```

**Change configuration (triggers rolling restart via config hash):**

```sh
kubectl -n cortex patch cortex test --type=merge -p '{"spec":{"authEnabled":false}}'
# Observe new pods rolling out with updated config hash annotation
kubectl -n cortex get pods -w
```

**Check generated config:**

```sh
kubectl -n cortex get configmap test-config -o jsonpath='{.data.cortex\.yaml}'
```

**Check CR status and conditions:**

```sh
kubectl -n cortex get cortex test -o yaml
```

### 5. Clean up

```sh
# Delete the Cortex CR (all owned resources are garbage collected)
kubectl -n cortex delete cortex test

# Delete the cluster
make kind-teardown
```

## CRD Reference

### CortexSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `image` | `ImageSpec` | — | Container image configuration (repository, tag, pullPolicy) |
| `authEnabled` | `*bool` | `true` | Enable multi-tenancy authentication |
| `storage` | `StorageSpec` | — | Object storage backend (s3, gcs, azure, swift) |
| `ring` | `*RingSpec` | — | Hash ring configuration |
| `memberlist` | `*MemberlistSpec` | — | Memberlist gossip configuration |
| `limits` | `*LimitsSpec` | — | Global rate limits |
| `runtimeConfig` | `*RuntimeConfigRef` | — | ConfigMap reference for per-tenant runtime overrides |
| `externalConfig` | `*ExternalConfigSpec` | — | Escape hatch: use a pre-existing ConfigMap instead of generated config |
| `distributor` | `*ComponentSpec` | replicas: 1 | Distributor component |
| `ingester` | `*IngesterComponentSpec` | replicas: 1, terminationGracePeriod: 2400s | Ingester component |
| `querier` | `*ComponentSpec` | replicas: 1 | Querier component |
| `queryFrontend` | `*ComponentSpec` | replicas: 1 | Query Frontend component |
| `storeGateway` | `*StoreGatewaySpec` | replicas: 1, shardingEnabled: true | Store Gateway component |
| `compactor` | `*CompactorComponentSpec` | replicas: 1 | Compactor component |

### ComponentSpec (shared fields)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `replicas` | `*int32` | `1` | Number of replicas |
| `resources` | `*ResourceRequirements` | — | CPU/memory requests and limits |
| `nodeSelector` | `map[string]string` | — | Node label selector for scheduling |
| `tolerations` | `[]Toleration` | — | Pod tolerations |
| `affinity` | `*Affinity` | — | Pod affinity/anti-affinity rules |
| `extraArgs` | `[]string` | — | Additional command-line arguments |
| `extraEnv` | `[]EnvVar` | — | Additional environment variables |

### StorageSpec

| Field | Type | Description |
|-------|------|-------------|
| `backend` | `string` | One of: `s3`, `gcs`, `azure`, `swift` |
| `s3` | `*S3StorageSpec` | S3 config: `bucketName`, `endpoint`, `region`, `insecure` |
| `gcs` | `*GCSStorageSpec` | GCS config: `bucketName` |
| `azure` | `*AzureStorageSpec` | Azure config: `containerName`, `accountName` |
| `swift` | `*SwiftStorageSpec` | Swift config: `containerName`, `authUrl` |
| `secretRef` | `*LocalObjectReference` | Secret with storage credentials (injected as env vars) |

### Status Conditions

| Condition | Description |
|-----------|-------------|
| `ConfigReady` | Configuration was generated successfully |
| `Ready` | All components have all replicas ready |
| `Degraded` | One or more components have zero ready replicas |

## Architecture

```
┌──────────────────────────────────────────────────┐
│                 Cortex CR (spec)                 │
└──────────────────┬───────────────────────────────┘
                   │
                   ▼
┌──────────────────────────────────────────────────┐
│              Cortex Controller                   │
│                                                  │
│  1. Generate config YAML (or use externalConfig) │
│  2. Create/Update ConfigMap                      │
│  3. Create/Update gossip Service                 │
│  4. Reconcile Ingester (headless svc + STS + PDB)│
│  5. Reconcile Store Gateway (svc + STS + PDB)    │
│  6. Reconcile Compactor (headless svc + STS + PDB)│
│  7. Reconcile Distributor (svc + Deployment)     │
│  8. Reconcile Querier (svc + Deployment)         │
│  9. Reconcile Query Frontend (svc + Deployment)  │
│ 10. Update CR status                             │
└──────────────────────────────────────────────────┘
```

All resources use OwnerReferences for automatic garbage collection when the CR is deleted.

Config changes produce a new SHA-256 hash stored as the `cortex.io/config-hash` pod annotation, triggering rolling restarts.

## Development

### Run tests

```sh
cd operator/
make test
```

This runs:
- Config builder unit tests (`internal/config/`)
- Controller envtest integration tests (`internal/controller/`)
- Webhook validation/defaulting tests (`internal/webhook/`)

### Build

```sh
make build           # Build the operator binary
make docker-build    # Build the Docker image
make manifests       # Regenerate CRD, RBAC, and webhook manifests
make generate        # Regenerate DeepCopy methods
```

### Run locally (outside cluster)

```sh
# Install CRDs first
bin/kustomize build config/crd | kubectl apply --server-side -f -

# Run the operator against the current kubeconfig
ENABLE_WEBHOOKS=false go run ./cmd/main.go
```

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
