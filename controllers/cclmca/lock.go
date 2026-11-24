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
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/stolostron/mtv-integrations/controllers"
)

const (
	leasePrefix         = "proxy-cclm-ca-exchange-"
	leaseReleaseTimeout = 10 * time.Second
	// leaseDuration outlives AdvisorHTTPWriteTimeout so a held lock cannot expire
	// while the HTTP handler is still running. RenewTime is refreshed until release.
	leaseDuration = AdvisorHTTPWriteTimeout + 30*time.Second
)

// leaseRenewInterval is how often a holder refreshes RenewTime. Tests shorten it.
var leaseRenewInterval = leaseDuration / 3

func leaseDurationSeconds() int32 {
	return int32(leaseDuration / time.Second)
}

var hubLeaseNamespace = controllers.MTVIntegrationsNamespace

func holderIdentity() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "mtv-integrations"
	}
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewUUID())
}

type clusterLock struct {
	client      kubernetes.Interface
	name        string
	holder      string
	acquired    bool
	cancelRenew context.CancelFunc
	renewDone   chan struct{}
}

var errLeaseLost = errors.New("lease holder changed")

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create;update

func acquireClusterLock(ctx context.Context, client kubernetes.Interface, cluster string) (*clusterLock, error) {
	holder := holderIdentity()
	name := leasePrefix + cluster
	now := metav1.NowMicro()
	dur := leaseDurationSeconds()

	_, err := client.CoordinationV1().Leases(hubLeaseNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: hubLeaseNamespace},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &dur,
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		_, err = client.CoordinationV1().Leases(hubLeaseNamespace).Create(ctx, lease, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create lease %s: %w", name, err)
		}
		if apierrors.IsAlreadyExists(err) {
			return tryTakeLease(ctx, client, name, holder)
		}
		return heldLock(client, name, holder), nil
	}
	if err != nil {
		return nil, fmt.Errorf("get lease %s: %w", name, err)
	}
	return tryTakeLease(ctx, client, name, holder)
}

func tryTakeLease(ctx context.Context, client kubernetes.Interface, name, holder string) (*clusterLock, error) {
	lease, err := client.CoordinationV1().Leases(hubLeaseNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" && *lease.Spec.HolderIdentity != holder {
		if !leaseExpired(lease) {
			return nil, nil
		}
	}
	now := metav1.NowMicro()
	dur := leaseDurationSeconds()
	lease.Spec.HolderIdentity = &holder
	lease.Spec.LeaseDurationSeconds = &dur
	lease.Spec.RenewTime = &now
	if lease.Spec.AcquireTime == nil || leaseExpired(lease) {
		lease.Spec.AcquireTime = &now
	}
	_, err = client.CoordinationV1().Leases(hubLeaseNamespace).Update(ctx, lease, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("update lease %s: %w", name, err)
	}
	return heldLock(client, name, holder), nil
}

func heldLock(client kubernetes.Interface, name, holder string) *clusterLock {
	lk := &clusterLock{client: client, name: name, holder: holder, acquired: true}
	lk.startRenew()
	return lk
}

func (l *clusterLock) startRenew() {
	ctx, cancel := context.WithCancel(context.Background())
	l.cancelRenew = cancel
	l.renewDone = make(chan struct{})
	go func() {
		defer close(l.renewDone)
		ticker := time.NewTicker(leaseRenewInterval)
		defer ticker.Stop()
		log := ctrl.LoggerFrom(ctx).WithName("proxy-cclm-ca-exchange-lock")
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := l.renew(context.Background()); err != nil {
					if errors.Is(err, errLeaseLost) {
						log.Error(err, "stop lease renew", "lease", l.name)
						return
					}
					log.Error(err, "renew lease", "lease", l.name)
				}
			}
		}
	}()
}

func (l *clusterLock) renew(ctx context.Context) error {
	lease, err := l.client.CoordinationV1().Leases(hubLeaseNamespace).Get(ctx, l.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != l.holder {
		return errLeaseLost
	}
	now := metav1.NowMicro()
	dur := leaseDurationSeconds()
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseDurationSeconds = &dur
	_, err = l.client.CoordinationV1().Leases(hubLeaseNamespace).Update(ctx, lease, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}

func (l *clusterLock) stopRenew() {
	if l == nil || l.cancelRenew == nil {
		return
	}
	l.cancelRenew()
	<-l.renewDone
	l.cancelRenew = nil
}

func leaseExpired(lease *coordinationv1.Lease) bool {
	if lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil {
		return true
	}
	expireAt := lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
	return time.Now().After(expireAt)
}

func (l *clusterLock) release(ctx context.Context) {
	if l == nil || !l.acquired {
		return
	}
	l.stopRenew()
	log := ctrl.LoggerFrom(ctx).WithName("proxy-cclm-ca-exchange-lock")
	lease, err := l.client.CoordinationV1().Leases(hubLeaseNamespace).Get(ctx, l.name, metav1.GetOptions{})
	if err != nil {
		log.Error(err, "release lease: get failed", "lease", l.name)
		return
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != l.holder {
		return
	}
	empty := ""
	lease.Spec.HolderIdentity = &empty
	_, err = l.client.CoordinationV1().Leases(hubLeaseNamespace).Update(ctx, lease, metav1.UpdateOptions{})
	if err != nil {
		log.Error(err, "release lease: update failed", "lease", l.name)
	}
}

// acquireLocksInOrder acquires one lease per cluster in sorted name order.
func acquireLocksInOrder(ctx context.Context, client kubernetes.Interface, clusters []string) ([]*clusterLock, error) {
	sorted := append([]string(nil), clusters...)
	sort.Strings(sorted)
	locks := make([]*clusterLock, 0, len(sorted))
	for _, c := range sorted {
		lk, err := acquireClusterLock(ctx, client, c)
		if err != nil {
			releaseAll(ctx, locks)
			return nil, err
		}
		if lk == nil || !lk.acquired {
			releaseAll(ctx, locks)
			return nil, errLockBusy
		}
		locks = append(locks, lk)
	}
	return locks, nil
}

func releaseAll(_ context.Context, locks []*clusterLock) {
	if len(locks) == 0 {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), leaseReleaseTimeout)
	defer cancel()
	for _, l := range locks {
		l.release(releaseCtx)
	}
}

var errLockBusy = fmt.Errorf("cluster CA exchange lock busy")
