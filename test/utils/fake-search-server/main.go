// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

const fakeSearchResponse = `{
  "data": {
    "search": [
      {
        "items": [
          {"cluster":"advisor-cluster","name":"ceph-rbd","provisioner":"rbd.csi.ceph.com"},
          {"cluster":"target-cluster","name":"ceph-rbd","provisioner":"rbd.csi.ceph.com"},
          {"cluster":"untarget-cluster","name":"gp2","provisioner":"ebs.csi.aws.com"}
        ]
      }
    ]
  }
}`

func main() {
	var port int
	flag.IntVar(&port, "port", 19091, "Port to serve fake Search API")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/searchapi/graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeSearchResponse))
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("fake search server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("fake search server exited: %v", err)
	}
}
