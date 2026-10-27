# Migration Advisor

This document explains how Migration Advisor works in `mtv-integrations`, including API behavior, data flow, scoring, and test setup.

## Overview

Migration Advisor provides an HTTP API that evaluates where a source VM can be migrated and returns:

- recommended target cluster/node
- ranked candidate clusters
- excluded clusters with reasons

The API is served by the manager process on `--advisor-addr` (default `:8082`).

## Endpoints

### `GET /api/v1/migration-targets`

Query params:

- `vmNamespace` (required)
- `vmName` (required)
- `cluster` (required, source managed cluster name)

Response fields:

- `sourceVM`
- `recommendation`
- `candidates`
- `excludedClusters`

Example response:

```json
{
  "sourceVM": {
    "name": "advisor-e2e-vm",
    "namespace": "default",
    "cluster": "advisor-cluster",
    "cpuCores": 2,
    "memoryBytes": 4294967296,
    "volumes": [
      {
        "name": "rootdisk",
        "storageClass": "ceph-rbd",
        "sizeBytes": 10737418240
      }
    ]
  },
  "recommendation": {
    "cluster": "target-cluster",
    "node": "node1",
    "totalScore": 72.25,
    "availableCpuCores": 7,
    "availableMemoryBytes": 15032385536,
    "storageType": "ceph",
    "cephAvailableBytes": 75161927680
  },
  "candidates": [
    {
      "cluster": "target-cluster",
      "totalScore": 72.25,
      "cpuScore": 62.5,
      "memoryScore": 67.19,
      "storageScore": 70,
      "matchedStorageClasses": [
        "ceph-rbd"
      ],
      "bestNode": "node1",
      "availableCpuCores": 7,
      "availableMemoryBytes": 15032385536,
      "storageType": "ceph",
      "cephAvailableBytes": 75161927680
    }
  ],
  "excludedClusters": [
    {
      "cluster": "untarget-cluster",
      "reason": "No matching StorageClass found on target cluster"
    }
  ]
}
```

If required query params are missing, returns `400`.
If evaluation fails, returns `500`.

### `GET /health`

Health checks Thanos connectivity through `ObservabilityClient.CheckHealth()`.

- `200` when Thanos query endpoint is reachable
- `503` when Thanos is unreachable/unhealthy

## Data Flow

Advisor handler runs two branches in parallel:

1. VM branch (always fresh)
   - Reads source VM and volume information via `ManagedClusterView`
   - Resolves PVC storage class names via additional `ManagedClusterView` objects

2. Cluster-wide branch (cached for 30s)
   - Reads node/Ceph metrics from Thanos
   - Reads StorageClass provisioners from ACM Search API
   - Filters clusters to only managed clusters labeled:
     - `acm/cnv-operator-install=true`

## Candidate and Excluded Logic

Before scoring, clusters are pre-filtered to managed clusters labeled:

- `acm/cnv-operator-install=true`

Scoring currently excludes clusters for three hard reasons:

- no matching storage class
- no node with sufficient CPU/memory
- insufficient storage capacity

Remaining clusters are scored (CPU, memory, storage weighted total) and sorted descending.

## Endpoint Discovery and Overrides

On each cluster-data refresh, advisor lists Search CRs and uses the CR namespace for the Search API service. If a `search-api` Route exists in that namespace, it uses the Route URL so the advisor can also run outside the cluster. When no Search CR is present, it uses the standard `open-cluster-management` namespace; a later Search installation is discovered on the next refresh. If multiple Search CRs exist, set `--search-api-endpoint` explicitly. The controller service account needs `list` on `searches.search.open-cluster-management.io`.

If a discovered Search endpoint cannot be reached, the advisor returns an error and retries discovery on the next cluster-data refresh. It does not silently use a different Search installation. An explicit `--search-api-endpoint` is used as given.

Thanos Route discovery continues to use `open-cluster-management-observability`.

Useful flags:

- `--search-api-endpoint=<url>`
- `--thanos-host=<url>`
- `--advisor-addr=<host:port>`

## E2E Testing

Migration advisor e2e tests are in:

- `test/e2e/migration_advisor_test.go`

### Test resources

- `test/resources/migration_advisor/managedcluster.yaml` (source)
- `test/resources/migration_advisor/target_managedcluster.yaml`
- `test/resources/migration_advisor/untarget_managecluster.yaml`

### Fake backends used by advisor e2e

- Fake Thanos server:
  - `test/utils/fake-thanos-server/main.go`
- Fake Search server:
  - `test/utils/fake-search-server/main.go`

`run-advisor-test` starts both fake servers and passes endpoint overrides into the instrumented manager.

### ManagedClusterView CRD

Advisor e2e requires `ManagedClusterView` CRD. It is installed from:

- https://raw.githubusercontent.com/stolostron/cluster-lifecycle-api/main/view/v1beta1/view.open-cluster-management.io_managedclusterviews.crd.yaml

## Running Advisor E2E

Typical flow:

```bash
make prepare-e2e-test
make run-advisor-test
```

This target starts:

- fake Search server
- fake Thanos server
- instrumented `mtv_integrations_instrumented` process

Then executes only `migration_advisor`-labeled specs.
