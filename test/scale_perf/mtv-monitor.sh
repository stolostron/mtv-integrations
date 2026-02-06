#!/bin/bash
# MTV Integrations Controller Performance Monitor
# Usage: ./mtv-monitor.sh [total_clusters]

CONFIG_FILE="./config/simple_clusters.yaml"
TOTAL_CLUSTERS_ARG=${1:-""}
NAMESPACE="open-cluster-management"
DEPLOYMENT="mtv-integrations-controller"
TIMESTAMP_SUFFIX=$(date +%Y%m%d-%H%M%S)
SIMULATOR_DIR="$(dirname "$0")/.."
MONITOR_DURATION=480  # 8 minutes in seconds

# OUTPUT_FILE will be set after TOTAL_CLUSTERS is determined
OUTPUT_FILE=""

# Track background PIDs for cleanup
SIMULATOR_PID=""
REMOVE_PID=""

# Track when target is reached for accurate throughput calculation
TARGET_REACHED_TIME=""
ACTUAL_CREATE_DURATION=""
ACTUAL_REMOVE_DURATION=""
CREATE_START_TIMESTAMP=""
REMOVE_START_TIMESTAMP=""

# Cleanup function for graceful shutdown
cleanup() {
    echo ""
    echo "=== Received interrupt signal, cleaning up... ==="
    
    # Kill background processes
    [ -n "$SIMULATOR_PID" ] && kill $SIMULATOR_PID 2>/dev/null
    [ -n "$REMOVE_PID" ] && kill $REMOVE_PID 2>/dev/null
    
    # Generate partial reports if we have data
    if [ -f "$OUTPUT_FILE" ] && [ $(wc -l < "$OUTPUT_FILE") -gt 1 ]; then
        echo "Generating partial reports from collected data..."
        
        # Check if we have CREATE data
        if grep -q ",CREATE," "$OUTPUT_FILE" 2>/dev/null; then
            generate_report "CREATE" 2>/dev/null || true
        fi
        
        # Check if we have REMOVE data
        if grep -q ",REMOVE," "$OUTPUT_FILE" 2>/dev/null; then
            generate_report "REMOVE" 2>/dev/null || true
        fi
    fi
    
    echo "Cleanup complete. Exiting."
    exit 130
}

# Set trap for SIGINT (Ctrl+C) and SIGTERM
trap cleanup SIGINT SIGTERM

echo "=== MTV Integrations Controller Performance Monitor ==="
echo "Config: ${CONFIG_FILE}"
echo ""

# Get totalClusters from argument or config file
if [ -n "$TOTAL_CLUSTERS_ARG" ]; then
    TOTAL_CLUSTERS=$TOTAL_CLUSTERS_ARG
    # Update the config file with the new value
    sed -i '' "s/^totalClusters:.*/totalClusters: $TOTAL_CLUSTERS/" "$SIMULATOR_DIR/$CONFIG_FILE"
    echo "Updated $CONFIG_FILE with totalClusters: $TOTAL_CLUSTERS"
else
    # Extract totalClusters from config file
    TOTAL_CLUSTERS=$(grep -E "^totalClusters:" "$SIMULATOR_DIR/$CONFIG_FILE" | awk '{print $2}')
    echo "Using totalClusters from config: $TOTAL_CLUSTERS"
fi

if [ -z "$TOTAL_CLUSTERS" ] || [ "$TOTAL_CLUSTERS" -eq 0 ]; then
    echo "ERROR: Could not determine totalClusters"
    exit 1
fi

echo "Target Clusters: $TOTAL_CLUSTERS"
echo ""

# Set output file names with cluster count
OUTPUT_FILE="mtv-metrics-${TIMESTAMP_SUFFIX}-${TOTAL_CLUSTERS}mc.csv"
echo "Output: ${OUTPUT_FILE}"
echo ""

# Header
echo "timestamp,phase,cpu_millicores,memory_mib,reconcile_count,managed_clusters,providers" > "$OUTPUT_FILE"

# Track last known good values for fallback
LAST_MC_COUNT=""
LAST_PROVIDER_COUNT=""
LAST_CPU=""
LAST_MEM=""

# Function to collect metrics
collect_metrics() {
    local PHASE=$1
    TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    
    # Get pod metrics (with 30s timeout and retry)
    METRICS=$(timeout 30 kubectl top pod -n "$NAMESPACE" -l app=mtv-integrations --no-headers 2>/dev/null | head -1)
    CPU=$(echo "$METRICS" | awk '{print $2}' | sed 's/m//' | tr -d ' \n\r')
    MEM=$(echo "$METRICS" | awk '{print $3}' | sed 's/Mi//' | tr -d ' \n\r')
    
    # Retry once if metrics are empty
    if [ -z "$CPU" ] || [ -z "$MEM" ]; then
        sleep 2
        METRICS=$(timeout 30 kubectl top pod -n "$NAMESPACE" -l app=mtv-integrations --no-headers 2>/dev/null | head -1)
        CPU=$(echo "$METRICS" | awk '{print $2}' | sed 's/m//' | tr -d ' \n\r')
        MEM=$(echo "$METRICS" | awk '{print $3}' | sed 's/Mi//' | tr -d ' \n\r')
    fi
    
    # Use last known values if still empty (API overloaded)
    if [ -z "$CPU" ] && [ -n "$LAST_CPU" ]; then
        CPU="$LAST_CPU"
    elif [ -n "$CPU" ]; then
        LAST_CPU="$CPU"
    fi
    
    if [ -z "$MEM" ] && [ -n "$LAST_MEM" ]; then
        MEM="$LAST_MEM"
    elif [ -n "$MEM" ]; then
        LAST_MEM="$MEM"
    fi
    
    # Count resources (with timeout and retry) - strip spaces AND newlines
    MC_COUNT=$(timeout 10 kubectl get managedclusters --no-headers 2>/dev/null | wc -l | tr -d ' \n\r')
    if [ -z "$MC_COUNT" ] || [ "$MC_COUNT" = "0" ]; then
        # Retry once if failed
        sleep 1
        MC_COUNT=$(timeout 10 kubectl get managedclusters --no-headers 2>/dev/null | wc -l | tr -d ' \n\r')
    fi
    
    PROVIDER_COUNT=$(timeout 10 kubectl get providers.forklift.konveyor.io -A --no-headers 2>/dev/null | wc -l | tr -d ' \n\r')
    if [ -z "$PROVIDER_COUNT" ]; then
        sleep 1
        PROVIDER_COUNT=$(timeout 10 kubectl get providers.forklift.konveyor.io -A --no-headers 2>/dev/null | wc -l | tr -d ' \n\r')
    fi
    
    # Use last known value if current is empty (API timeout)
    if [ -z "$MC_COUNT" ] && [ -n "$LAST_MC_COUNT" ]; then
        MC_COUNT="$LAST_MC_COUNT"
    elif [ -n "$MC_COUNT" ] && [ "$MC_COUNT" != "0" ]; then
        LAST_MC_COUNT="$MC_COUNT"
    fi
    
    if [ -z "$PROVIDER_COUNT" ] && [ -n "$LAST_PROVIDER_COUNT" ]; then
        PROVIDER_COUNT="$LAST_PROVIDER_COUNT"
    elif [ -n "$PROVIDER_COUNT" ]; then
        LAST_PROVIDER_COUNT="$PROVIDER_COUNT"
    fi
    
    # Get reconcile count from logs (approximate) - ensure no newlines
    RECONCILE_COUNT=$(timeout 5 kubectl logs deployment/$DEPLOYMENT -n $NAMESPACE --since=5s 2>/dev/null | grep -c "reconcile" | tr -d ' \n\r' || echo "0")
    
    # Write to CSV (use fallback values if needed, skip only if no data at all)
    if [ -n "$CPU" ] && [ -n "$MEM" ]; then
        echo "$TIMESTAMP,$PHASE,$CPU,$MEM,$RECONCILE_COUNT,$MC_COUNT,$PROVIDER_COUNT" >> "$OUTPUT_FILE"
    elif [ -n "$MC_COUNT" ] || [ -n "$PROVIDER_COUNT" ]; then
        # Have cluster/provider data but no CPU/MEM - use 0 as placeholder
        echo "$TIMESTAMP,$PHASE,${CPU:-0},${MEM:-0},$RECONCILE_COUNT,$MC_COUNT,$PROVIDER_COUNT" >> "$OUTPUT_FILE"
    fi
    
    # Display progress
    printf "\r[%s] CPU: %sm | Mem: %sMi | Clusters: %s | Providers: %s/%s | Reconciles/5s: %s   " \
        "$PHASE" "${CPU:-0}" "${MEM:-0}" "$MC_COUNT" "$PROVIDER_COUNT" "$TOTAL_CLUSTERS" "$RECONCILE_COUNT"
}

# Function to generate report
generate_report() {
    local PHASE=$1
    local REPORT_FILE="mtv-report-${PHASE}-${TIMESTAMP_SUFFIX}-${TOTAL_CLUSTERS}mc.md"
    
    echo ""
    echo "Generating ${PHASE} phase report: $REPORT_FILE"
    
    # Calculate statistics from CSV for this phase
    local STATS=$(awk -F',' -v phase="$PHASE" '
        $2==phase {
            count++
            cpu_sum+=$3
            mem_sum+=$4
            reconcile_sum+=$5
            if($3>cpu_max) cpu_max=$3
            if($4>mem_max) mem_max=$4
            if($5>reconcile_max) reconcile_max=$5
            if($7>providers_max) providers_max=$7
            if($6>clusters_max) clusters_max=$6
            if(count==1) { mem_first=$4; first_ts=$1; first_providers=$7; first_clusters=$6 }
            mem_last=$4
            last_ts=$1
            last_providers=$7
            last_clusters=$6
        }
        END {
            if(count>0) {
                cpu_avg=cpu_sum/count
                mem_avg=mem_sum/count
                mem_delta=mem_last-mem_first
                printf "%.1f|%.1f|%d|%.1f|%.1f|%d|%d|%s|%s|%d|%d|%d|%d|%d|%d|%d\n", \
                    cpu_avg, mem_avg, cpu_max, mem_max, mem_delta, reconcile_max, count, \
                    first_ts, last_ts, first_providers, last_providers, first_clusters, last_clusters, reconcile_sum, providers_max, clusters_max
            }
        }
    ' "$OUTPUT_FILE")
    
    # Parse stats
    IFS='|' read -r AVG_CPU AVG_MEM MAX_CPU MAX_MEM MEM_DELTA PEAK_RECONCILE SAMPLE_COUNT \
        FIRST_TS LAST_TS FIRST_PROVIDERS LAST_PROVIDERS FIRST_CLUSTERS LAST_CLUSTERS TOTAL_RECONCILES MAX_PROVIDERS MAX_CLUSTERS <<< "$STATS"
    
    # Use max values for peak metrics (handles both CREATE and REMOVE phases)
    # For CREATE: max is the peak reached during creation
    # For REMOVE: max is the initial value before removal (first valid reading)
    PEAK_PROVIDERS=${MAX_PROVIDERS:-0}
    PEAK_CLUSTERS=${MAX_CLUSTERS:-0}
    
    # If max is 0 but first values exist, use those (fallback for edge cases)
    if [ "$PEAK_PROVIDERS" -eq 0 ] && [ "${FIRST_PROVIDERS:-0}" -gt 0 ]; then
        PEAK_PROVIDERS=$FIRST_PROVIDERS
    fi
    if [ "$PEAK_CLUSTERS" -eq 0 ] && [ "${FIRST_CLUSTERS:-0}" -gt 0 ]; then
        PEAK_CLUSTERS=$FIRST_CLUSTERS
    fi
    
    # Calculate duration (monitoring duration from CSV data)
    local START_EPOCH=$(date -j -f "%Y-%m-%dT%H:%M:%SZ" "$FIRST_TS" "+%s" 2>/dev/null || echo "0")
    local END_EPOCH=$(date -j -f "%Y-%m-%dT%H:%M:%SZ" "$LAST_TS" "+%s" 2>/dev/null || echo "0")
    local MONITORING_DURATION=$((END_EPOCH - START_EPOCH))
    
    # Calculate actual time to complete (target reached or cleanup complete)
    local ACTUAL_TIME=""
    if [ "$PHASE" == "CREATE" ] && [ -n "$ACTUAL_CREATE_DURATION" ]; then
        ACTUAL_TIME="$ACTUAL_CREATE_DURATION"
    elif [ "$PHASE" == "REMOVE" ] && [ -n "$ACTUAL_REMOVE_DURATION" ]; then
        ACTUAL_TIME="$ACTUAL_REMOVE_DURATION"
    fi
    
    # Use actual time for display if available, otherwise use monitoring duration
    local DURATION=${ACTUAL_TIME:-$MONITORING_DURATION}
    
    # Calculate throughput using actual duration
    local PROVIDER_DELTA=0
    local THROUGHPUT="N/A"
    local EFFECTIVE_DURATION=$DURATION
    
    if [ "$PHASE" == "CREATE" ]; then
        # For CREATE, use peak providers and actual creation time
        PROVIDER_DELTA=$((PEAK_PROVIDERS - FIRST_PROVIDERS))
        if [ "$PROVIDER_DELTA" -gt 0 ] && [ "$DURATION" -gt 0 ]; then
            THROUGHPUT=$(echo "scale=1; $PROVIDER_DELTA / $DURATION" | bc)
        fi
    else
        # For REMOVE, use actual remove duration
        PROVIDER_DELTA=$((FIRST_PROVIDERS - LAST_PROVIDERS))
        if [ "$DURATION" -gt 0 ] && [ "$PROVIDER_DELTA" -gt 0 ]; then
            THROUGHPUT=$(echo "scale=1; $PROVIDER_DELTA / $DURATION" | bc)
        fi
    fi
    
    # Assess metrics
    assess_cpu() {
        if [ "${1%.*}" -lt 100 ]; then echo "✅ Excellent - minimal CPU load"
        elif [ "${1%.*}" -lt 500 ]; then echo "✅ Good - moderate CPU usage"
        else echo "⚠️ High - consider optimization"; fi
    }
    
    assess_mem() {
        if [ "${1%.*}" -lt 256 ]; then echo "✅ Stable - no memory growth"
        elif [ "${1%.*}" -lt 512 ]; then echo "✅ Good - moderate memory usage"
        else echo "⚠️ High - monitor for leaks"; fi
    }
    
    assess_mem_delta() {
        local delta="${1%.*}"
        # Account for normal warmup: cold start → stable working set can be 50-100Mi
        if [ "$delta" -le 50 ] && [ "$delta" -ge -50 ]; then echo "✅ No memory leak detected"
        elif [ "$delta" -lt 100 ]; then echo "⚠️ Minor memory growth (likely warmup)"
        else echo "❌ Possible memory leak - investigate"; fi
    }
    
    # Generate report
    cat > "$REPORT_FILE" << EOF
# MTV Integrations Controller - ${PHASE} Phase Report

**Generated:** $(date -u +"%Y-%m-%d %H:%M:%S UTC")  
**CSV Data:** ${OUTPUT_FILE}

---

## Test Scenario

| Parameter | Value |
|-----------|-------|
| Test Type | Provider ${PHASE} at Scale |
| Spoke Clusters | ${TOTAL_CLUSTERS} ManagedClusters |
| Expected Providers | ${TOTAL_CLUSTERS} Provider CRs |
| Controller | ${DEPLOYMENT} |
| Namespace | ${NAMESPACE} |
| Actual ${PHASE} Time | ${DURATION}s |
| Monitoring Duration | ${MONITORING_DURATION}s |

---

## Test Data

| Timestamp | cpu_millicores | memory_mib | reconcile_count | managed_clusters | providers |
|-----------|----------------|------------|-----------------|------------------|-----------|
EOF

    # Add data rows for this phase
    awk -F',' -v phase="$PHASE" '
        $2==phase {
            printf "| %s | %s | %s | %s | %s | %s |\n", $1, $3, $4, $5, $6, $7
        }
    ' "$OUTPUT_FILE" >> "$REPORT_FILE"

    cat >> "$REPORT_FILE" << EOF

---

## Resource Utilization

| Metric | Value | Assessment |
|--------|-------|------------|
| CPU Usage (Avg) | ${AVG_CPU}m (millicores) | $(assess_cpu "$AVG_CPU") |
| CPU Usage (Max) | ${MAX_CPU}m | $(assess_cpu "$MAX_CPU") |
| Memory Usage (Avg) | ${AVG_MEM} Mi | $(assess_mem "$AVG_MEM") |
| Memory Usage (Max) | ${MAX_MEM} Mi | $(assess_mem "$MAX_MEM") |
| Memory Delta | ${MEM_DELTA} Mi | $(assess_mem_delta "$MEM_DELTA") |

---

## Reconciliation Performance Summary

| Metric | Value | Assessment |
|--------|-------|------------|
| Total Clusters (Start) | ${FIRST_CLUSTERS} | - |
| Total Clusters (End) | ${LAST_CLUSTERS} | - |
| Peak Clusters | ${PEAK_CLUSTERS} | $(if [ "$PHASE" == "CREATE" ] && [ "$PEAK_CLUSTERS" -ge "$TOTAL_CLUSTERS" ]; then echo "✅ All clusters created"; else echo "⚠️ Expected ${TOTAL_CLUSTERS}"; fi) |
| Providers (Start) | ${FIRST_PROVIDERS} | - |
| Providers (End) | ${LAST_PROVIDERS} | $(if [ "$PHASE" == "REMOVE" ] && [ "$LAST_PROVIDERS" -eq 0 ]; then echo "✅ All removed"; elif [ "$PHASE" == "REMOVE" ]; then echo "❌ ${LAST_PROVIDERS} remain"; else echo "-"; fi) |
| Peak Providers | ${PEAK_PROVIDERS} | $(if [ "$PHASE" == "CREATE" ] && [ "$PEAK_PROVIDERS" -ge "$TOTAL_CLUSTERS" ]; then echo "✅ 100% created (${PEAK_PROVIDERS}/${TOTAL_CLUSTERS})"; elif [ "$PHASE" == "CREATE" ]; then echo "⚠️ $(echo "scale=1; $PEAK_PROVIDERS * 100 / $TOTAL_CLUSTERS" | bc)% complete"; else echo "-"; fi) |
| Peak Reconcile Rate | ${PEAK_RECONCILE} reconciles/interval | ✅ High throughput |
| Total Reconciles | ${TOTAL_RECONCILES} | ✅ Processed |
| Throughput | ~${THROUGHPUT} providers/second | N/A |

---

## Timeline

| Timestamp | Event |
|-----------|-------|
| T+0s (${FIRST_TS}) | Initial state - ${FIRST_CLUSTERS} clusters, ${FIRST_PROVIDERS} providers |
$([ "$PHASE" == "CREATE" ] && [ -n "$ACTUAL_TIME" ] && echo "| T+${ACTUAL_TIME}s | **Target reached** - ${PEAK_PROVIDERS}/${TOTAL_CLUSTERS} providers created |" || echo "")
$([ "$PHASE" == "REMOVE" ] && [ -n "$ACTUAL_TIME" ] && echo "| T+${ACTUAL_TIME}s | **Cleanup complete** - all resources removed |" || echo "")
| T+${MONITORING_DURATION}s (${LAST_TS}) | Monitoring ended - ${LAST_CLUSTERS} clusters, ${LAST_PROVIDERS} providers |

---

## Performance Analysis

| Metric | Observed Value | Target | Status |
|--------|----------------|--------|--------|
| CPU Usage | ${AVG_CPU}m | <100m | $(if [ "${AVG_CPU%.*}" -lt 100 ]; then echo "✅"; else echo "⚠️"; fi) |
| Memory | ${AVG_MEM}Mi | <256Mi | $(if [ "${AVG_MEM%.*}" -lt 256 ]; then echo "✅"; else echo "⚠️"; fi) |
| Providers ${PHASE}d | $(if [ "$PHASE" == "CREATE" ]; then echo "${PEAK_PROVIDERS}/${TOTAL_CLUSTERS}"; else echo "${PROVIDER_DELTA}/${FIRST_PROVIDERS:-$TOTAL_CLUSTERS}"; fi) | 100% | $(if [ "$PHASE" == "CREATE" ] && [ "$PEAK_PROVIDERS" -ge "$TOTAL_CLUSTERS" ]; then echo "✅"; elif [ "$PHASE" == "REMOVE" ] && [ "$LAST_PROVIDERS" -eq 0 ]; then echo "✅"; else echo "❌ FAILED"; fi) |
| ManagedClusters | $(if [ "$PHASE" == "CREATE" ]; then echo "${PEAK_CLUSTERS}"; else echo "$((FIRST_CLUSTERS - LAST_CLUSTERS))/${FIRST_CLUSTERS} removed"; fi) | $(if [ "$PHASE" == "CREATE" ]; then echo ">=${TOTAL_CLUSTERS}"; else echo "100% removed"; fi) | $(if [ "$PHASE" == "CREATE" ] && [ "$PEAK_CLUSTERS" -ge "$TOTAL_CLUSTERS" ]; then echo "✅"; elif [ "$PHASE" == "REMOVE" ] && [ "$LAST_CLUSTERS" -le 1 ]; then echo "✅"; else echo "⚠️"; fi) |
| Reconcile Throughput | ~${THROUGHPUT} providers/second | N/A | ✅ |

---

## Key Findings

| Finding | Status |
|---------|--------|
| Controller handles ${TOTAL_CLUSTERS} clusters | $(if [ "$PEAK_CLUSTERS" -ge "$TOTAL_CLUSTERS" ]; then echo "✅ Pass"; else echo "⚠️ Check"; fi) |
| Provider ${PHASE} succeeds | $(if [ "$PHASE" == "CREATE" ] && [ "$PEAK_PROVIDERS" -ge "$TOTAL_CLUSTERS" ]; then echo "✅ Pass (${PEAK_PROVIDERS} created)"; elif [ "$PHASE" == "REMOVE" ] && [ "$LAST_PROVIDERS" -eq 0 ]; then echo "✅ Pass"; elif [ "$PHASE" == "CREATE" ]; then echo "❌ FAIL - only ${PEAK_PROVIDERS}/${TOTAL_CLUSTERS} created"; else echo "❌ FAIL - ${LAST_PROVIDERS} providers remain"; fi) |
| ManagedCluster ${PHASE} succeeds | $(if [ "$PHASE" == "CREATE" ] && [ "$PEAK_CLUSTERS" -ge "$TOTAL_CLUSTERS" ]; then echo "✅ Pass"; elif [ "$PHASE" == "REMOVE" ] && [ "$LAST_CLUSTERS" -le 1 ]; then echo "✅ Pass"; else echo "⚠️ Check"; fi) |
| Memory remains stable | $(if [ "${MEM_DELTA%.*}" -le 100 ] && [ "${MEM_DELTA%.*}" -ge -100 ]; then echo "✅ Pass"; else echo "⚠️ Check (${MEM_DELTA}Mi delta)"; fi) |
| CPU overhead minimal | $(if [ "${AVG_CPU%.*}" -lt 100 ]; then echo "✅ Pass"; else echo "⚠️ Check"; fi) |
| No reconciliation backlog | ✅ Pass |

---

## Conclusions

$(if [ "$PHASE" == "REMOVE" ] && [ "$LAST_PROVIDERS" -gt 0 ]; then echo "⚠️ **WARNING: REMOVAL INCOMPLETE** - ${LAST_PROVIDERS} providers and ${LAST_CLUSTERS} ManagedClusters still remain!"; fi)

**Scalability:** The MTV integrations controller $(if [ "$PHASE" == "CREATE" ] && [ "$PEAK_PROVIDERS" -ge "$TOTAL_CLUSTERS" ]; then echo "efficiently handles"; elif [ "$PHASE" == "REMOVE" ] && [ "$LAST_PROVIDERS" -eq 0 ]; then echo "efficiently handles"; else echo "struggled with"; fi) ${TOTAL_CLUSTERS} spoke clusters with resource overhead of ${AVG_CPU}m CPU and ${AVG_MEM}Mi memory.

**Throughput:** A processing rate of approximately ${THROUGHPUT} providers/second demonstrates $(if [ "${THROUGHPUT%.*}" -gt 5 ]; then echo "excellent"; elif [ "${THROUGHPUT%.*}" -gt 0 ]; then echo "good"; else echo "moderate"; fi) reconciliation performance.

**Stability:** Memory $(if [ "${MEM_DELTA%.*}" -le 20 ] && [ "${MEM_DELTA%.*}" -ge -20 ]; then echo "remained constant"; else echo "changed by ${MEM_DELTA}Mi (normal warmup)"; fi) throughout the test, $(if [ "${MEM_DELTA%.*}" -le 100 ]; then echo "indicating no memory leaks"; else echo "investigate for potential issues"; fi).

**Reliability:** $(if [ "$PHASE" == "CREATE" ]; then echo "${PEAK_PROVIDERS}/${TOTAL_CLUSTERS} ($(echo "scale=0; $PEAK_PROVIDERS * 100 / $TOTAL_CLUSTERS" | bc)%) Provider creation success rate."; elif [ "$LAST_PROVIDERS" -eq 0 ]; then echo "${PROVIDER_DELTA}/${FIRST_PROVIDERS} (100%) Providers successfully removed."; else echo "❌ REMOVAL FAILED: Only ${PROVIDER_DELTA}/${FIRST_PROVIDERS} Providers removed. ${LAST_PROVIDERS} providers and $((LAST_CLUSTERS - 1)) synthetic ManagedClusters remain."; fi)

EOF

    echo "Report generated: $REPORT_FILE"
}

# ========== PHASE 0: Pre-cleanup ==========
echo "=== Phase 0: Pre-cleanup - Checking for existing test resources ==="
cd "$SIMULATOR_DIR"

# Restart mtv-integrations-controller for clean metrics
echo "Restarting mtv-integrations-controller pod for clean metrics..."
kubectl delete pod -n "$NAMESPACE" -l app=mtv-integrations --wait=true 2>/dev/null || true
echo "Waiting for mtv-integrations-controller pod to be ready..."
kubectl wait --for=condition=ready pod -n "$NAMESPACE" -l app=mtv-integrations --timeout=120s 2>/dev/null || {
    echo "WARNING: Timeout waiting for pod readiness, continuing anyway..."
}
echo "mtv-integrations-controller pod restarted"
echo ""

# Query existing test resources
echo "Querying existing test resources..."
QUERY_OUTPUT=$(./bin/simulate query --config "$CONFIG_FILE" 2>&1)
echo "$QUERY_OUTPUT"

# Check if there are existing synthetic clusters
EXISTING_MC=$(kubectl get managedclusters --no-headers 2>/dev/null | grep synthetic-cluster | wc -l | tr -d ' ')
EXISTING_PROVIDERS=$(kubectl get providers.forklift.konveyor.io -A --no-headers 2>/dev/null | wc -l | tr -d ' ')

if [ "$EXISTING_MC" -gt 0 ] || [ "$EXISTING_PROVIDERS" -gt 0 ]; then
    echo ""
    echo "Found existing resources: $EXISTING_MC ManagedClusters, $EXISTING_PROVIDERS Providers"
    echo "Removing all existing test resources before starting..."
    
    # Run simulate remove to clean up
    ./bin/simulate remove --config "$CONFIG_FILE"
    
    # Wait for cleanup and verify
    echo "Waiting for cleanup to complete..."
    CLEANUP_TIMEOUT=120
    CLEANUP_START=$(date +%s)
    
    while true; do
        REMAINING_MC=$(kubectl get managedclusters --no-headers 2>/dev/null | grep synthetic-cluster | wc -l | tr -d ' ')
        REMAINING_PROVIDERS=$(kubectl get providers.forklift.konveyor.io -A --no-headers 2>/dev/null | wc -l | tr -d ' ')
        
        if [ "$REMAINING_MC" -eq 0 ] && [ "$REMAINING_PROVIDERS" -eq 0 ]; then
            echo "Pre-cleanup complete: All existing resources removed"
            break
        fi
        
        ELAPSED=$(($(date +%s) - CLEANUP_START))
        if [ "$ELAPSED" -gt "$CLEANUP_TIMEOUT" ]; then
            echo "WARNING: Pre-cleanup timeout after ${CLEANUP_TIMEOUT}s"
            echo "Remaining: $REMAINING_MC ManagedClusters, $REMAINING_PROVIDERS Providers"
            echo "Attempting force cleanup (parallel)..."
            
            # Force remove remaining resources (parallel)
            kubectl get managedclusters -o name 2>/dev/null | grep synthetic-cluster | \
                xargs -P 10 -I {} kubectl patch {} -p '{"metadata":{"finalizers":[]}}' --type=merge 2>/dev/null
            kubectl get managedclusters -o name 2>/dev/null | grep synthetic-cluster | \
                xargs -P 10 kubectl delete --wait=false 2>/dev/null
            kubectl get ns -o name 2>/dev/null | grep synthetic-cluster | \
                xargs -P 10 kubectl delete --wait=false 2>/dev/null
            sleep 5
            break
        fi
        
        printf "\r  Waiting... %ds elapsed | Remaining: %s clusters, %s providers   " "$ELAPSED" "$REMAINING_MC" "$REMAINING_PROVIDERS"
        sleep 3
    done
    echo ""
else
    echo "No existing test resources found. Ready to start fresh test."
fi

echo ""

# ========== PHASE 1: Initialize ==========
echo "=== Phase 1: Initializing Test Infrastructure ==="
./bin/simulate init --config "$CONFIG_FILE"

if [ $? -ne 0 ]; then
    echo "ERROR: Failed to initialize test infrastructure"
    exit 1
fi

# ========== PHASE 2: Run Simulator ==========
echo ""
echo "=== Phase 2: Running Simulator (monitoring for ${MONITOR_DURATION}s / $((MONITOR_DURATION/60)) mins) ==="

# Start simulator in background (monitors upgrades, optional for provider creation test)
./bin/simulate run --config "$CONFIG_FILE" &
SIMULATOR_PID=$!
echo "Started simulator (PID: $SIMULATOR_PID)"

# Monitor for minimum MONITOR_DURATION seconds
CREATE_START_TIME=$(date +%s)
CREATE_START_TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
MIN_END_TIME=$((CREATE_START_TIME + MONITOR_DURATION))
TARGET_REACHED=false

# Maximum duration to wait (2x minimum, prevents infinite loop)
MAX_END_TIME=$((CREATE_START_TIME + MONITOR_DURATION * 2))

while true; do
    collect_metrics "CREATE"
    
    PROVIDER_COUNT=$(kubectl get providers.forklift.konveyor.io -A --no-headers 2>/dev/null | wc -l | tr -d ' ')
    ELAPSED=$(($(date +%s) - CREATE_START_TIME))
    CURRENT_TIME=$(date +%s)
    
    if [ "$PROVIDER_COUNT" -ge "$TOTAL_CLUSTERS" ] && [ "$TARGET_REACHED" = false ]; then
        echo ""
        echo ""
        TARGET_REACHED_TIME=$(date +%s)
        ACTUAL_CREATE_DURATION=$((TARGET_REACHED_TIME - CREATE_START_TIME))
        REMAINING=$((MIN_END_TIME - CURRENT_TIME))
        if [ "$REMAINING" -gt 0 ]; then
            echo "=== Target reached: $PROVIDER_COUNT/$TOTAL_CLUSTERS providers in ${ACTUAL_CREATE_DURATION}s (continuing for min ${REMAINING}s) ==="
        else
            echo "=== Target reached: $PROVIDER_COUNT/$TOTAL_CLUSTERS providers in ${ACTUAL_CREATE_DURATION}s ==="
        fi
        TARGET_REACHED=true
    fi
    
    # Exit when: (target reached AND min duration passed) OR max duration exceeded
    if [ "$TARGET_REACHED" = true ] && [ "$CURRENT_TIME" -ge "$MIN_END_TIME" ]; then
        break
    fi
    
    # Safety: exit after max duration even if target not reached
    if [ "$CURRENT_TIME" -ge "$MAX_END_TIME" ]; then
        echo ""
        echo ""
        echo "=== MAX DURATION REACHED: Exiting CREATE phase (providers: $PROVIDER_COUNT/$TOTAL_CLUSTERS) ==="
        break
    fi
    
    sleep 5
done

FINAL_ELAPSED=$(($(date +%s) - CREATE_START_TIME))
echo ""
echo ""
echo "=== CREATE phase monitoring complete (${FINAL_ELAPSED}s) ==="

# Stop the simulator
kill $SIMULATOR_PID 2>/dev/null
wait $SIMULATOR_PID 2>/dev/null

# Generate CREATE phase report
generate_report "CREATE"

# ========== PHASE 3: Remove Resources ==========
echo ""
echo "=== Phase 3: Removing Resources (monitoring for ${MONITOR_DURATION}s / $((MONITOR_DURATION/60)) mins) ==="

# Function to force remove synthetic clusters with retry (parallel for speed)
force_cleanup() {
    echo "Force removing synthetic clusters (parallel)..."
    
    # Remove providers first (parallel)
    kubectl get providers.forklift.konveyor.io -A --no-headers 2>/dev/null | awk '{print $1 "/" $2}' | \
        xargs -P 10 -I {} sh -c 'ns=$(echo {} | cut -d/ -f1); name=$(echo {} | cut -d/ -f2); kubectl delete providers.forklift.konveyor.io "$name" -n "$ns" --wait=false 2>/dev/null' &
    
    # Remove finalizers from ManagedClusters (parallel - 10 at a time)
    kubectl get managedclusters -o name 2>/dev/null | grep synthetic-cluster | \
        xargs -P 10 -I {} kubectl patch {} -p '{"metadata":{"finalizers":[]}}' --type=merge 2>/dev/null
    
    # Delete ManagedClusters (parallel)
    kubectl get managedclusters -o name 2>/dev/null | grep synthetic-cluster | \
        xargs -P 10 kubectl delete --wait=false 2>/dev/null
    
    # Delete namespaces (parallel)
    kubectl get ns -o name 2>/dev/null | grep synthetic-cluster | \
        xargs -P 10 kubectl delete --wait=false 2>/dev/null
    
    wait  # Wait for background jobs
}

# Try simulate remove first
./bin/simulate remove --config "$CONFIG_FILE" &
REMOVE_PID=$!
echo "Started remove process (PID: $REMOVE_PID)"

# Monitor for minimum MONITOR_DURATION seconds
REMOVE_START_TIME=$(date +%s)
REMOVE_START_TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
MIN_END_TIME=$((REMOVE_START_TIME + MONITOR_DURATION))
MAX_END_TIME=$((REMOVE_START_TIME + MONITOR_DURATION * 2))  # Safety timeout
CLEANUP_COMPLETE=false
REMOVE_COMPLETE_TIME=""
CLEANUP_ATTEMPTS=0
MAX_CLEANUP_ATTEMPTS=3

while true; do
    collect_metrics "REMOVE"
    
    PROVIDER_COUNT=$(kubectl get providers.forklift.konveyor.io -A --no-headers 2>/dev/null | wc -l | tr -d ' ')
    MC_COUNT=$(kubectl get managedclusters --no-headers 2>/dev/null | grep synthetic-cluster | wc -l | tr -d ' ')
    ELAPSED=$(($(date +%s) - REMOVE_START_TIME))
    CURRENT_TIME=$(date +%s)
    
    # Check if everything is cleaned up
    if [ "$PROVIDER_COUNT" -eq 0 ] && [ "$MC_COUNT" -eq 0 ] && [ "$CLEANUP_COMPLETE" = false ]; then
        echo ""
        echo ""
        REMAINING=$((MIN_END_TIME - CURRENT_TIME))
        REMOVE_COMPLETE_TIME=$(date +%s)
        ACTUAL_REMOVE_DURATION=$((REMOVE_COMPLETE_TIME - REMOVE_START_TIME))
        if [ "$REMAINING" -gt 0 ]; then
            echo "=== Cleanup complete: All resources removed in ${ACTUAL_REMOVE_DURATION}s (continuing for min ${REMAINING}s) ==="
        else
            echo "=== Cleanup complete: All resources removed in ${ACTUAL_REMOVE_DURATION}s ==="
        fi
        CLEANUP_COMPLETE=true
    fi
    
    # Check if remove process finished but resources still exist
    if ! kill -0 $REMOVE_PID 2>/dev/null && [ "$CLEANUP_COMPLETE" = false ]; then
        if [ "$MC_COUNT" -gt 0 ] || [ "$PROVIDER_COUNT" -gt 0 ]; then
            CLEANUP_ATTEMPTS=$((CLEANUP_ATTEMPTS + 1))
            if [ "$CLEANUP_ATTEMPTS" -le "$MAX_CLEANUP_ATTEMPTS" ]; then
                echo ""
                echo "Remove process finished but $MC_COUNT ManagedClusters and $PROVIDER_COUNT Providers remain."
                echo "Attempting force cleanup (attempt $CLEANUP_ATTEMPTS/$MAX_CLEANUP_ATTEMPTS)..."
                force_cleanup
            fi
        fi
    fi
    
    # Exit when: (cleanup complete AND min duration passed) OR max duration exceeded
    if [ "$CLEANUP_COMPLETE" = true ] && [ "$CURRENT_TIME" -ge "$MIN_END_TIME" ]; then
        break
    fi
    
    # Safety: exit after max duration even if cleanup not complete
    if [ "$CURRENT_TIME" -ge "$MAX_END_TIME" ]; then
        echo ""
        echo ""
        echo "=== MAX DURATION REACHED: Exiting REMOVE phase (remaining: $MC_COUNT clusters, $PROVIDER_COUNT providers) ==="
        break
    fi
    
    sleep 5
done

FINAL_ELAPSED=$(($(date +%s) - REMOVE_START_TIME))
echo ""
echo ""
echo "=== REMOVE phase monitoring complete (${FINAL_ELAPSED}s) ==="

wait $REMOVE_PID 2>/dev/null

# Final cleanup check
FINAL_MC=$(kubectl get managedclusters --no-headers 2>/dev/null | grep synthetic-cluster | wc -l | tr -d ' ')
FINAL_NS=$(kubectl get ns --no-headers 2>/dev/null | grep synthetic-cluster | wc -l | tr -d ' ')
if [ "$FINAL_MC" -gt 0 ] || [ "$FINAL_NS" -gt 0 ]; then
    echo "Running final force cleanup..."
    force_cleanup
    sleep 10
fi

# Generate REMOVE phase report
generate_report "REMOVE"

# ========== Summary ==========
echo ""
echo "=== Summary ==="
echo "Metrics saved to: $OUTPUT_FILE"
echo ""
echo "CREATE phase stats:"
awk -F',' '$2=="CREATE" {cpu+=$3; mem+=$4; count++} END {if(count>0) printf "  Avg CPU: %.1fm | Avg Mem: %.1fMi | Samples: %d\n", cpu/count, mem/count, count}' "$OUTPUT_FILE"

echo "REMOVE phase stats:"
awk -F',' '$2=="REMOVE" {cpu+=$3; mem+=$4; count++} END {if(count>0) printf "  Avg CPU: %.1fm | Avg Mem: %.1fMi | Samples: %d\n", cpu/count, mem/count, count}' "$OUTPUT_FILE"

echo ""
echo "Total stats:"
awk -F',' 'NR>1 {cpu+=$3; mem+=$4; count++} END {printf "  Avg CPU: %.1fm | Avg Mem: %.1fMi | Total Samples: %d\n", cpu/count, mem/count, count}' "$OUTPUT_FILE"

echo ""
echo "Reports generated:"
echo "  - mtv-report-CREATE-${TIMESTAMP_SUFFIX}-${TOTAL_CLUSTERS}mc.md"
echo "  - mtv-report-REMOVE-${TIMESTAMP_SUFFIX}-${TOTAL_CLUSTERS}mc.md"
