// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cclmca

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAcquireClusterLock_SecondCallerBusy(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	first, err := acquireClusterLock(ctx, client, "cluster-a")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if first == nil || !first.acquired {
		t.Fatal("expected first lock acquired")
	}
	defer first.release(ctx)

	second, err := acquireClusterLock(ctx, client, "cluster-a")
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second != nil && second.acquired {
		t.Fatal("expected second acquire to fail while first lock is held")
	}
	if second != nil {
		t.Fatal("expected nil lock when lease is held by another holder")
	}

	leases, err := client.CoordinationV1().Leases(hubLeaseNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list leases: %v", err)
	}
	if len(leases.Items) != 1 {
		t.Fatalf("expected one lease, got %d", len(leases.Items))
	}
	holder := leases.Items[0].Spec.HolderIdentity
	if holder == nil || *holder != first.holder {
		t.Fatalf("lease holder=%v want %q", holder, first.holder)
	}
}

func TestAcquireClusterLock_CreatesLeaseInNamespace(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	lk, err := acquireClusterLock(ctx, client, "spoke-1")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lk.release(ctx)
	_, err = client.CoordinationV1().Leases(hubLeaseNamespace).Get(ctx, leasePrefix+"spoke-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
}

func TestLeaseExpired(t *testing.T) {
	dur := int32(1)
	renew := metav1.NewMicroTime(metav1.NowMicro().Add(-2 * time.Second))
	lease := &coordinationv1.Lease{
		Spec: coordinationv1.LeaseSpec{
			LeaseDurationSeconds: &dur,
			RenewTime:            &renew,
		},
	}
	if !leaseExpired(lease) {
		t.Fatal("expected expired lease")
	}
}

func TestLeaseDurationOutlivesAdvisorWrite(t *testing.T) {
	if leaseDuration <= AdvisorHTTPWriteTimeout {
		t.Fatalf("lease duration %s must exceed advisor write timeout %s", leaseDuration, AdvisorHTTPWriteTimeout)
	}
}

func TestClusterLockRenewsWhileHeld(t *testing.T) {
	orig := leaseRenewInterval
	leaseRenewInterval = 20 * time.Millisecond
	t.Cleanup(func() { leaseRenewInterval = orig })

	ctx := context.Background()
	client := fake.NewSimpleClientset()
	lk, err := acquireClusterLock(ctx, client, "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	defer lk.release(ctx)

	lease, err := client.CoordinationV1().Leases(hubLeaseNamespace).Get(
		ctx, leasePrefix+"cluster-a", metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	first := lease.Spec.RenewTime.Time
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lease, err = client.CoordinationV1().Leases(hubLeaseNamespace).Get(
			ctx, leasePrefix+"cluster-a", metav1.GetOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
		if lease.Spec.RenewTime != nil && lease.Spec.RenewTime.After(first) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected RenewTime to advance while the lock is held")
}
