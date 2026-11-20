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
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/stolostron/mtv-integrations/controllers"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const mergePatchMetadataKey = "metadata"

var managedClusterGVR = schema.GroupVersionResource{
	Group:    "cluster.open-cluster-management.io",
	Version:  "v1",
	Resource: "managedclusters",
}

// SpokeConfigMapClient reads and patches ConfigMaps on a managed cluster.
type SpokeConfigMapClient interface {
	GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error)
	PatchConfigMap(
		ctx context.Context, namespace, name string, patchType types.PatchType, data []byte,
	) (*corev1.ConfigMap, error)
}

// KubeVirtNamespace returns the namespace where KubeVirt ConfigMaps live.
func KubeVirtNamespace(override string) string {
	if ns := strings.TrimSpace(override); ns != "" {
		return ns
	}
	if ns := strings.TrimSpace(os.Getenv("MTV_KUBEVIRT_NAMESPACE")); ns != "" {
		return ns
	}
	return defaultKubeVirtNamespace
}

type spokeClient struct {
	clientset kubernetes.Interface
}

type ctxCallerBearerKey struct{}

// WithCallerBearerToken stores the API caller's bearer token for spoke client construction.
func WithCallerBearerToken(ctx context.Context, bearer string) context.Context {
	return context.WithValue(ctx, ctxCallerBearerKey{}, bearer)
}

func callerBearerFromContext(ctx context.Context) (string, bool) {
	bearer, ok := ctx.Value(ctxCallerBearerKey{}).(string)
	return bearer, ok && strings.TrimSpace(bearer) != ""
}

func validateSpokeHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse spoke API URL: %w", err)
	}
	if u.Scheme != "https" || strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("spoke API URL must use https with a host")
	}
	return nil
}

// spokeAPIEndpoint resolves the managed cluster API server URL and optional CA bundle.
func spokeAPIEndpoint(
	ctx context.Context,
	hub kubernetes.Interface,
	dynamicClient dynamic.Interface,
	clusterName string,
) (string, []byte, error) {
	secret, err := hub.CoreV1().Secrets(controllers.MTVIntegrationsNamespace).
		Get(ctx, clusterName+"-mtv", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return "", nil, fmt.Errorf("cluster %q provider secret: %w", clusterName, err)
	}
	var apiURL string
	var ca []byte
	if err == nil {
		apiURL = string(secret.Data["url"])
		ca = secret.Data["cacert"]
	}

	mc, err := dynamicClient.Resource(managedClusterGVR).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("get ManagedCluster %q: %w", clusterName, err)
	}
	configs, found, err := unstructured.NestedSlice(mc.Object, "spec", "managedClusterClientConfigs")
	if err != nil {
		return "", nil, fmt.Errorf("decode ManagedCluster %q client configs: %w", clusterName, err)
	}
	if found && len(configs) > 0 {
		if cfg, ok := configs[0].(map[string]interface{}); ok {
			u, _, nestedErr := unstructured.NestedString(cfg, "url")
			if nestedErr != nil {
				return "", nil, fmt.Errorf("decode ManagedCluster %q client url: %w", clusterName, nestedErr)
			}
			if u != "" {
				apiURL = u
			}
		}
	}
	if apiURL == "" {
		return "", nil, fmt.Errorf("cluster %q has no spoke API url (provider secret or ManagedCluster)", clusterName)
	}
	if err := validateSpokeHTTPSURL(apiURL); err != nil {
		return "", nil, fmt.Errorf("cluster %q: %w", clusterName, err)
	}
	return apiURL, ca, nil
}

// NewSpokeClientForCaller builds a spoke client using the caller's bearer token (not the MTV provider secret token).
func NewSpokeClientForCaller(
	ctx context.Context,
	hub kubernetes.Interface,
	dynamicClient dynamic.Interface,
	clusterName string,
	callerBearer string,
) (SpokeConfigMapClient, error) {
	if strings.TrimSpace(callerBearer) == "" {
		return nil, fmt.Errorf("caller bearer token required for spoke access")
	}
	apiURL, ca, err := spokeAPIEndpoint(ctx, hub, dynamicClient, clusterName)
	if err != nil {
		return nil, err
	}

	cfg := &rest.Config{
		Host:        apiURL,
		BearerToken: callerBearer,
	}
	if len(ca) > 0 {
		cfg.TLSClientConfig = rest.TLSClientConfig{CAData: ca}
	} else {
		cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: false}
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build spoke client for %q: %w", clusterName, err)
	}
	return &spokeClient{clientset: cs}, nil
}

func (s *spokeClient) GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	return s.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (s *spokeClient) PatchConfigMap(
	ctx context.Context,
	namespace, name string,
	patchType types.PatchType,
	data []byte,
) (*corev1.ConfigMap, error) {
	return s.clientset.CoreV1().ConfigMaps(namespace).Patch(ctx, name, patchType, data, metav1.PatchOptions{})
}

func readCABundle(cm *corev1.ConfigMap) ([]byte, error) {
	if cm == nil || cm.Data == nil {
		return nil, nil
	}
	return []byte(cm.Data[caBundleDataKey]), nil
}

// errKubevirtExternalCANotFound means the spoke has no kubevirt-external-ca ConfigMap yet.
// The API updates an existing ConfigMap only; operators must create it (KubeVirt CCLM setup).
var errKubevirtExternalCANotFound = errors.New("kubevirt-external-ca ConfigMap not found")

func patchKubevirtCABundle(
	ctx context.Context,
	client SpokeConfigMapClient,
	namespace string,
	bundle []byte,
) error {
	cm, err := client.GetConfigMap(ctx, namespace, kubevirtCAConfigMapName)
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]interface{}{
		mergePatchMetadataKey: map[string]interface{}{
			"resourceVersion": cm.ResourceVersion,
		},
		"data": map[string]string{caBundleDataKey: string(bundle)},
	})
	if err != nil {
		return fmt.Errorf("marshal kubevirt-ca patch: %w", err)
	}
	_, err = client.PatchConfigMap(ctx, namespace, kubevirtCAConfigMapName, types.MergePatchType, patch)
	if apierrors.IsConflict(err) {
		return fmt.Errorf("%w: %v", errConfigMapConflict, err)
	}
	return err
}

// ensureSinglePrimaryCA clears an oversized kubevirt-ca ca-bundle, then waits for the CNV operator
// to publish a fresh single-certificate bundle before kubevirt-external-ca is written.
func ensureSinglePrimaryCA(
	ctx context.Context,
	client SpokeConfigMapClient,
	namespace string,
	timeout time.Duration,
) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	clearIssued := false
	for {
		bundle, err := readClusterCABundle(ctx, client, namespace)
		if err != nil {
			return err
		}
		count, err := certificateCount(bundle)
		if err != nil {
			return err
		}
		if count == 1 {
			return nil
		}
		if count > 1 && !clearIssued {
			if err := patchKubevirtCABundle(ctx, client, namespace, nil); err != nil {
				if errors.Is(err, errConfigMapConflict) {
					if waitErr := waitTickOrDone(waitCtx, ticker); waitErr != nil {
						return waitErr
					}
					continue
				}
				return err
			}
			clearIssued = true
		}
		if waitErr := waitTickOrDone(waitCtx, ticker); waitErr != nil {
			if errors.Is(waitErr, context.DeadlineExceeded) {
				if count > 1 {
					return fmt.Errorf(
						"%w: kubevirt-ca still has %d certificates after clear; CNV operator did not reconcile to one",
						waitErr, count,
					)
				}
				return fmt.Errorf("%w: kubevirt-ca empty; waiting for CNV operator to publish fresh CA bundle", waitErr)
			}
			return waitErr
		}
	}
}

func waitTickOrDone(ctx context.Context, ticker *time.Ticker) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ticker.C:
		return nil
	}
}

func upsertExternalCABundle(
	ctx context.Context,
	client SpokeConfigMapClient,
	namespace string,
	bundle []byte,
) error {
	cm, err := client.GetConfigMap(ctx, namespace, kubevirtExternalCAConfigMapName)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w", errKubevirtExternalCANotFound)
	}
	if err != nil {
		return err
	}
	existing, err := readCABundle(cm)
	if err != nil {
		return err
	}
	if pendingExternalCABundle(string(existing)) {
		return fmt.Errorf("%w: external CA bundle is pending", errConfigMapConflict)
	}
	patch, err := json.Marshal(map[string]interface{}{
		mergePatchMetadataKey: map[string]interface{}{
			"resourceVersion": cm.ResourceVersion,
		},
		"data": map[string]string{caBundleDataKey: string(bundle)},
	})
	if err != nil {
		return fmt.Errorf("marshal external CA patch: %w", err)
	}
	_, err = client.PatchConfigMap(ctx, namespace, kubevirtExternalCAConfigMapName, types.MergePatchType, patch)
	if apierrors.IsConflict(err) {
		return fmt.Errorf("%w: %v", errConfigMapConflict, err)
	}
	return err
}

var errConfigMapConflict = fmt.Errorf("configmap resource version conflict")

func pollTrustEstablished(
	ctx context.Context,
	client SpokeConfigMapClient,
	namespace string,
	expected map[string]struct{},
	interval time.Duration,
) (bool, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		cm, err := client.GetConfigMap(ctx, namespace, kubevirtCAConfigMapName)
		if err != nil {
			return false, err
		}
		bundle, err := readCABundle(cm)
		if err != nil {
			return false, err
		}
		ok, err := fingerprintsPresent(string(bundle), expected)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}