// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

const (
	targetCluster   = "target-cluster"
	targetNode      = "node1"
	untargetCluster = "untarget-cluster"
	untargetNode    = "node2"
)

func promNodeResponse(targetValue, untargetValue string) string {
	return fmt.Sprintf(
		`{"status":"success","data":{"resultType":"vector","result":[`+
			`{"metric":{"cluster":%q,"node":%q},"value":[1234567890,%q]},`+
			`{"metric":{"cluster":%q,"node":%q},"value":[1234567890,%q]}]}}`,
		targetCluster, targetNode, targetValue,
		untargetCluster, untargetNode, untargetValue,
	)
}

func promClusterResponse(targetValue, untargetValue string) string {
	return fmt.Sprintf(
		`{"status":"success","data":{"resultType":"vector","result":[`+
			`{"metric":{"cluster":%q},"value":[1234567890,%q]},`+
			`{"metric":{"cluster":%q},"value":[1234567890,%q]}]}}`,
		targetCluster, targetValue,
		untargetCluster, untargetValue,
	)
}

func main() {
	var port int
	flag.IntVar(&port, "port", 19090, "Port to serve fake Thanos API")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// The query parameter is used only for routing in this test binary;
		// no length/content validation is intentional (test-only server).
		query := r.URL.Query().Get("query")

		body := `{"status":"success","data":{"resultType":"vector","result":[]}}`
		switch {
		case query == "up":
			body = promNodeResponse("1", "1")
		case strings.Contains(query, "kube_node_labels"):
			// Both nodes are schedulable (kubevirt.io/schedulable=true).
			body = promNodeResponse("1", "1")
		case strings.Contains(query, `kube_node_status_allocatable{resource="cpu"}`):
			body = promNodeResponse("8", "2")
		case strings.Contains(query, `kube_node_status_allocatable{resource="memory"}`):
			body = promNodeResponse("17179869184", "4294967296") // 16Gi / 4Gi
		case strings.Contains(query, `kube_pod_container_resource_requests{resource="cpu"}`):
			body = promNodeResponse("1", "3")
		case strings.Contains(query, `kube_pod_container_resource_requests{resource="memory"}`):
			body = promNodeResponse("2147483648", "5368709120") // 2Gi / 5Gi
		case strings.Contains(query, "ceph_cluster_total_bytes"):
			body = promClusterResponse("107374182400", "42949672960") // 100Gi / 40Gi
		case strings.Contains(query, "ceph_cluster_total_avail_bytes"):
			body = promClusterResponse("75161927680", "5368709120") // 70Gi / 5Gi
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("fake thanos server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("fake thanos server exited: %v", err)
	}
}
