# MTV Monitor - Performance Testing Tool

The `mtv-monitor.sh` script is an automated performance testing tool for the MTV Integrations Controller. It creates synthetic ManagedClusters, monitors Provider creation, collects metrics, and generates comprehensive reports.

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Usage](#usage)
- [How It Works](#how-it-works)
- [Output Files](#output-files)
- [Report Contents](#report-contents)
- [Examples](#examples)
- [Troubleshooting](#troubleshooting)

---

## Prerequisites

Before running the script, ensure:

1. **Simulator is built:**
   ```bash
   # Build ./bin/simulator (this Makefile fetches only the upstream simulator sources and builds it)
   cd test/scale_perf
   make build

   # Verify
   ./bin/simulator --help
   ```

2. **kubectl is configured** with access to your cluster

3. **MTV Integrations Controller** is deployed in `open-cluster-management` namespace

4. **Metrics Server** is running (for `kubectl top` commands)

---

## Quick Start

```bash
cd test/scale_perf

# Run with 100 clusters
./mtv-monitor.sh 100

# Run with value from simple_clusters.yaml
./mtv-monitor.sh
```

---

## Usage

```bash
./mtv-monitor.sh [total_clusters]
```

### Arguments

| Argument | Description | Default |
|----------|-------------|---------|
| `total_clusters` | Number of ManagedClusters to create | Value from `simple_clusters.yaml` |

### Examples

```bash
# Use totalClusters from config file (e.g., 500)
./mtv-monitor.sh

# Override with 100 clusters
./mtv-monitor.sh 100

# Test with 1000 clusters
./mtv-monitor.sh 1000
```

When you provide a `total_clusters` argument, the script automatically updates `config/simple_clusters.yaml` with the new value.

---

## How It Works

The script runs through 3 phases automatically:

### Phase 1: Initialize

```
=== Phase 1: Initializing Test Infrastructure ===
```

- Runs `./bin/simulate init --config ./config/simple_clusters.yaml`
- Creates synthetic Namespaces and ManagedClusters

### Phase 2: Create (Monitor Provider Creation)

```
=== Phase 2: Running Simulator (waiting for 100 providers) ===
[CREATE] CPU: 15m | Mem: 128Mi | Clusters: 100 | Providers: 50/100 | Reconciles/5s: 25
```

- Runs `./bin/simulate run --config ./config/simple_clusters.yaml` in background
- Collects metrics every 5 seconds
- Waits until Provider count matches `totalClusters`
- Generates **CREATE phase report**

### Phase 3: Remove (Monitor Cleanup)

```
=== Phase 3: Removing Resources (monitoring cleanup) ===
[REMOVE] CPU: 20m | Mem: 130Mi | Clusters: 100 | Providers: 50/100 | Reconciles/5s: 30
```

- Runs `./bin/simulate remove --config ./config/simple_clusters.yaml` in background
- Continues collecting metrics during cleanup
- Waits until all Providers are removed
- Generates **REMOVE phase report**

---

## Output Files

The script generates 3 files in the current directory:

| File | Description |
|------|-------------|
| `mtv-metrics-YYYYMMDD-HHMMSS.csv` | Raw metrics data (both phases) |
| `mtv-report-CREATE-YYYYMMDD-HHMMSS.md` | CREATE phase performance report |
| `mtv-report-REMOVE-YYYYMMDD-HHMMSS.md` | REMOVE phase performance report |

### CSV Format

```csv
timestamp,phase,cpu_millicores,memory_mib,reconcile_count,managed_clusters,providers
2026-02-05T16:58:17Z,CREATE,15,128,0,101,50
2026-02-05T16:58:22Z,CREATE,18,128,25,101,75
2026-02-05T16:58:27Z,CREATE,12,128,20,101,100
2026-02-05T16:58:32Z,REMOVE,22,130,30,101,80
...
```

---

## Report Contents

Each generated report includes:

### 1. Test Scenario

| Parameter | Value |
|-----------|-------|
| Test Type | Provider CREATE at Scale |
| Spoke Clusters | 100 ManagedClusters |
| Expected Providers | 100 Provider CRs |
| Controller | mtv-integrations-controller |
| Namespace | open-cluster-management |
| Duration | 45s |

### 2. Test Data

Full table of all collected metrics with timestamps.

### 3. Resource Utilization

| Metric | Value | Assessment |
|--------|-------|------------|
| CPU Usage (Avg) | 15m | ✅ Excellent - minimal CPU load |
| Memory Usage (Avg) | 128 Mi | ✅ Stable - no memory growth |
| Memory Delta | 0 Mi | ✅ No memory leak detected |

### 4. Reconciliation Performance Summary

| Metric | Value | Assessment |
|--------|-------|------------|
| Total Clusters | 101 | ✅ All detected |
| Providers Created | 100 | ✅ 100% success rate |
| Peak Reconcile Rate | 25 reconciles/interval | ✅ High throughput |
| Throughput | ~15 providers/second | N/A |

### 5. Timeline

| Timestamp | Event |
|-----------|-------|
| T+0s | Initial state - 101 clusters, 0 providers |
| T+45s | Final state - 101 clusters, 100 providers |

### 6. Performance Analysis

| Metric | Observed Value | Target | Status |
|--------|----------------|--------|--------|
| CPU Usage | 15m | <100m | ✅ |
| Memory | 128Mi | <256Mi | ✅ |
| Providers | 100/100 | 100% created | ✅ |

### 7. Key Findings

| Finding | Status |
|---------|--------|
| Controller handles 100 clusters | ✅ Pass |
| Provider creation succeeds | ✅ Pass |
| Memory remains stable | ✅ Pass |
| CPU overhead minimal | ✅ Pass |

### 8. Conclusions

Summary of scalability, throughput, stability, and reliability findings.

---

## Examples

### Example 1: Quick 100-cluster test

```bash
cd test/scale_perf
./mtv-monitor.sh 100
```

**Output:**
```
=== MTV Integrations Controller Performance Monitor ===
Config: ./config/simple_clusters.yaml
Output: mtv-metrics-20260205-170000.csv

Updated ./config/simple_clusters.yaml with totalClusters: 100
Target Clusters: 100

=== Phase 1: Initializing Test Infrastructure ===
Creating 100 synthetic clusters...
✓ Created 100 ManagedClusters

=== Phase 2: Running Simulator (waiting for 100 providers) ===
[CREATE] CPU: 15m | Mem: 128Mi | Clusters: 100 | Providers: 100/100 | Reconciles/5s: 0

=== Target reached: 100/100 providers created ===

Generating CREATE phase report: mtv-report-CREATE-20260205-170000.md
Report generated: mtv-report-CREATE-20260205-170000.md

=== Phase 3: Removing Resources (monitoring cleanup) ===
[REMOVE] CPU: 20m | Mem: 130Mi | Clusters: 100 | Providers: 0/100 | Reconciles/5s: 0

=== Cleanup complete: All providers removed ===

Generating REMOVE phase report: mtv-report-REMOVE-20260205-170000.md
Report generated: mtv-report-REMOVE-20260205-170000.md

=== Summary ===
Metrics saved to: mtv-metrics-20260205-170000.csv

CREATE phase stats:
  Avg CPU: 15.0m | Avg Mem: 128.0Mi | Samples: 10

REMOVE phase stats:
  Avg CPU: 18.0m | Avg Mem: 129.0Mi | Samples: 8

Total stats:
  Avg CPU: 16.3m | Avg Mem: 128.4Mi | Total Samples: 18

Reports generated:
  - mtv-report-CREATE-20260205-170000.md
  - mtv-report-REMOVE-20260205-170000.md
```

### Example 2: Scale test with 500 clusters

```bash
./mtv-monitor.sh 500
```

### Example 3: Use existing config value

```bash
# First, edit config/simple_clusters.yaml manually
vim config/simple_clusters.yaml
# Set totalClusters: 250

# Then run without argument
./mtv-monitor.sh
```

---

## Troubleshooting

### Error: Could not determine totalClusters

**Cause:** The config file is missing or `totalClusters` is not set.

**Solution:**
```bash
# Check config file exists
cat config/simple_clusters.yaml

# Ensure it has totalClusters
echo "totalClusters: 100" > config/simple_clusters.yaml
```

### Error: Failed to initialize test infrastructure

**Cause:** Simulator not built or kubectl not configured.

**Solution:**
```bash
# Build simulator
make build

# Check kubectl access
kubectl get nodes
```

### Metrics show 0 for CPU/Memory

**Cause:** Metrics server not running or pod label mismatch.

**Solution:**
```bash
# Check metrics server
kubectl top nodes

# Check pod labels
kubectl get pods -n open-cluster-management -l app=mtv-integrations
```

### Script hangs waiting for providers

**Cause:** MTV controller not creating providers.

**Solution:**
```bash
# Check controller logs
kubectl logs deployment/mtv-integrations-controller -n open-cluster-management

# Check if ManagedClusters have required labels
kubectl get managedclusters --show-labels
```

---

## Configuration

The script uses `config/simple_clusters.yaml`:

```yaml
totalClusters: 100
labels:
    - key: environment
      values:
        - value: production
          percentage: 60
        - value: staging
          percentage: 40
    - key: acm/cnv-operator-install
      values:
        - value: "true"
          percentage: 100
baseVersions:
    - version: 4.21.0
      percentage: 100
```

The `totalClusters` value is automatically updated when you pass an argument to the script.
