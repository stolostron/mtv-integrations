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
	"encoding/pem"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type fakeSpoke struct {
	mu  sync.Mutex
	cms map[string]*corev1.ConfigMap // key ns/name
	// simulateMerge copies external bundle into kubevirt-ca after delay hook
	onUpdateExternal func(cluster string)
	conflictNext     bool
	disableMerge     bool
	// operatorCA is republished into kubevirt-ca after a clear (simulate CNV operator).
	operatorCA string
}

func newFakeSpoke(initial map[string]map[string]string) *fakeSpoke {
	f := &fakeSpoke{cms: map[string]*corev1.ConfigMap{}}
	for ns, byName := range initial {
		for name, bundle := range byName {
			key := ns + "/" + name
			f.cms[key] = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, ResourceVersion: "1"},
				Data:       map[string]string{caBundleDataKey: bundle},
			}
		}
	}
	return f
}

func (f *fakeSpoke) GetConfigMap(_ context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cm, ok := f.cms[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("configmaps"), name)
	}
	return cm.DeepCopy(), nil
}

func (f *fakeSpoke) PatchConfigMap(
	_ context.Context,
	namespace, name string,
	_ types.PatchType,
	data []byte,
) (*corev1.ConfigMap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conflictNext {
		f.conflictNext = false
		return nil, apierrors.NewConflict(corev1.Resource("configmaps"), name, errConfigMapConflict)
	}
	key := namespace + "/" + name
	existing, ok := f.cms[key]
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("configmaps"), name)
	}
	var patch struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(data, &patch); err != nil {
		return nil, err
	}
	if rv := patch.Metadata.ResourceVersion; rv != "" && rv != existing.ResourceVersion {
		return nil, apierrors.NewConflict(corev1.Resource("configmaps"), name, errConfigMapConflict)
	}
	out := existing.DeepCopy()
	if out.Data == nil {
		out.Data = map[string]string{}
	}
	for k, v := range patch.Data {
		out.Data[k] = v
	}
	out.ResourceVersion = existing.ResourceVersion + "+1"
	f.cms[key] = out
	f.maybeRefreshKubevirtCA(out)
	f.maybeMergeExternal(out)
	return out.DeepCopy(), nil
}

func (f *fakeSpoke) maybeRefreshKubevirtCA(cm *corev1.ConfigMap) {
	if cm.Name != kubevirtCAConfigMapName || f.operatorCA == "" {
		return
	}
	if strings.TrimSpace(cm.Data[caBundleDataKey]) != "" {
		return
	}
	cm.Data[caBundleDataKey] = f.operatorCA
}

func (f *fakeSpoke) maybeMergeExternal(cm *corev1.ConfigMap) {
	if f.disableMerge || cm.Name != kubevirtExternalCAConfigMapName {
		return
	}
	ext := cm.Data[caBundleDataKey]
	if !pendingExternalCABundle(ext) {
		return
	}
	caKey := cm.Namespace + "/" + kubevirtCAConfigMapName
	caCM := f.cms[caKey]
	if caCM == nil {
		caCM = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: kubevirtCAConfigMapName, Namespace: cm.Namespace},
			Data:       map[string]string{},
		}
		f.cms[caKey] = caCM
	}
	merged, _ := mergePEMBundles(caCM.Data[caBundleDataKey], ext)
	caCM.Data[caBundleDataKey] = string(merged)
	cm.Data[caBundleDataKey] = ""
	if f.onUpdateExternal != nil {
		f.onUpdateExternal(cm.Namespace)
	}
}

func mergePEMBundles(existing, added string) ([]byte, error) {
	set := map[string]struct{}{}
	var blocks []string
	for _, b := range []string{existing, added} {
		certs, err := parseCertificates([]byte(b))
		if err != nil {
			return nil, err
		}
		for _, c := range certs {
			fp := certFingerprint(c)
			if _, ok := set[fp]; ok {
				continue
			}
			set[fp] = struct{}{}
			blocks = append(blocks, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})))
		}
	}
	out := ""
	for _, s := range blocks {
		out += s
	}
	return []byte(out), nil
}
