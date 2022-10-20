/*
Copyright 2022 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package replication

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"

	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = apisv1alpha1.AddToScheme(scheme)
}

func TestReconcileAPIExports(t *testing.T) {
	scenarios := []struct {
		name                                     string
		initialLocalApiExports                   []runtime.Object
		initialCacheApiExports                   []runtime.Object
		initCacheFakeClientWithInitialApiExports bool
		reconcileKey                             string
		validateFunc                             func(ts *testing.T, cacheClientActions []kcptesting.Action, localClientActions []kcptesting.Action)
	}{
		{
			name:                   "case 1: creation of the object in the cache server",
			initialLocalApiExports: []runtime.Object{newAPIExport("foo")},
			reconcileKey:           fmt.Sprintf("%s::root|foo", apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")),
			validateFunc: func(ts *testing.T, cacheClientActions []kcptesting.Action, localClientActions []kcptesting.Action) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("create", "apiexports") {
						createAction := action.(kcptesting.CreateAction)
						if createAction.GetCluster().String() != "root" {
							ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", createAction.GetCluster())
						}
						createdUnstructuredApiExport := createAction.GetObject().(*unstructured.Unstructured)
						cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(createdUnstructuredApiExport.Object, cacheApiExportFromUnstructured); err != nil {
							ts.Fatalf("failed to convert unstructured to APIExport: %v", err)
						}

						expectedApiExport := newAPIExportWithShardAnnotation("foo")
						if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
							ts.Errorf("unexpected ApiExport was creaetd:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't created")
				}
			},
		},
		{
			name: "case 2: cached object is removed when local object was removed",
			initialLocalApiExports: []runtime.Object{
				func() *apisv1alpha1.APIExport {
					t := metav1.NewTime(time.Now())
					apiExport := newAPIExport("foo")
					apiExport.DeletionTimestamp = &t
					apiExport.Finalizers = []string{"aFinalizer"}
					return apiExport
				}(),
			},
			initialCacheApiExports:                   []runtime.Object{newAPIExportWithShardAnnotation("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             fmt.Sprintf("%s::root|foo", apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")),
			validateFunc: func(ts *testing.T, cacheClientActions []kcptesting.Action, localClientActions []kcptesting.Action) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("delete", "apiexports") {
						deleteAction := action.(kcptesting.DeleteAction)
						if deleteAction.GetCluster().String() != "root" {
							ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", deleteAction.GetCluster())
						}
						if deleteAction.GetName() != "foo" {
							ts.Fatalf("unexpected APIExport was removed = %v, expected = %v", deleteAction.GetName(), "foo")
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't deleted")
				}
			},
		},
		{
			name:                                     "case 2: cached object is removed when local object was not found",
			initialCacheApiExports:                   []runtime.Object{newAPIExportWithShardAnnotation("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             fmt.Sprintf("%s::root|foo", apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")),
			validateFunc: func(ts *testing.T, cacheClientActions []kcptesting.Action, localClientActions []kcptesting.Action) {
				wasCacheApiExportDeletionValidated := false
				wasCacheApiExportRetrievalValidated := false
				for _, action := range localClientActions {
					if action.Matches("get", "apiexports") {
						getAction := action.(kcptesting.GetAction)
						if getAction.GetCluster().String() != "root" {
							ts.Fatalf("wrong cluster = %s was targeted for localDynamicClient", getAction.GetCluster())
						}
						if getAction.GetName() != "foo" {
							ts.Fatalf("unexpected ApiExport was retrieved = %s, expected = %s", getAction.GetName(), "foo")
						}
						wasCacheApiExportRetrievalValidated = true
						break
					}
				}
				if !wasCacheApiExportRetrievalValidated {
					ts.Errorf("before deleting an ApiExport the controller should live GET it")
				}
				for _, action := range cacheClientActions {
					if action.Matches("delete", "apiexports") {
						deleteAction := action.(kcptesting.DeleteAction)
						if deleteAction.GetCluster().String() != "root" {
							ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", deleteAction.GetCluster())
						}
						if deleteAction.GetName() != "foo" {
							ts.Fatalf("unexpected APIExport was removed = %v, expected = %v", deleteAction.GetName(), "foo")
						}
						wasCacheApiExportDeletionValidated = true
						break
					}
				}
				if !wasCacheApiExportDeletionValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't deleted")
				}
			},
		},
		{
			name: "case 3: update, metadata mismatch",
			initialLocalApiExports: []runtime.Object{
				func() *apisv1alpha1.APIExport {
					apiExport := newAPIExport("foo")
					apiExport.Labels["fooLabel"] = "fooLabelVal"
					return apiExport
				}(),
			},
			initialCacheApiExports:                   []runtime.Object{newAPIExportWithShardAnnotation("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             fmt.Sprintf("%s::root|foo", apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")),
			validateFunc: func(ts *testing.T, cacheClientActions []kcptesting.Action, localClientActions []kcptesting.Action) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("update", "apiexports") {
						updateAction := action.(kcptesting.UpdateAction)
						if updateAction.GetCluster().String() != "root" {
							ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", updateAction.GetCluster())
						}
						updatedUnstructuredApiExport := updateAction.GetObject().(*unstructured.Unstructured)
						cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(updatedUnstructuredApiExport.Object, cacheApiExportFromUnstructured); err != nil {
							ts.Fatalf("failed to convert unstructured to APIExport: %v", err)
						}

						expectedApiExport := newAPIExportWithShardAnnotation("foo")
						expectedApiExport.Labels["fooLabel"] = "fooLabelVal"
						if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
							ts.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
		{
			name: "case 3: update, spec changed",
			initialLocalApiExports: []runtime.Object{
				func() *apisv1alpha1.APIExport {
					apiExport := newAPIExport("foo")
					apiExport.Spec.PermissionClaims = []apisv1alpha1.PermissionClaim{{GroupResource: apisv1alpha1.GroupResource{}, IdentityHash: "abc"}}
					return apiExport
				}(),
			},
			initialCacheApiExports:                   []runtime.Object{newAPIExportWithShardAnnotation("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             fmt.Sprintf("%s::root|foo", apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")),
			validateFunc: func(ts *testing.T, cacheClientActions []kcptesting.Action, localClientActions []kcptesting.Action) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("update", "apiexports") {
						updateAction := action.(kcptesting.UpdateAction)
						if updateAction.GetCluster().String() != "root" {
							ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", updateAction.GetCluster())
						}
						updatedUnstructuredApiExport := updateAction.GetObject().(*unstructured.Unstructured)
						cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(updatedUnstructuredApiExport.Object, cacheApiExportFromUnstructured); err != nil {
							ts.Fatalf("failed to convert unstructured to APIExport: %v", err)
						}

						expectedApiExport := newAPIExportWithShardAnnotation("foo")
						expectedApiExport.Spec.PermissionClaims = []apisv1alpha1.PermissionClaim{{GroupResource: apisv1alpha1.GroupResource{}, IdentityHash: "abc"}}
						if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
							ts.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
		{
			name: "case 3: update, status changed",
			initialLocalApiExports: []runtime.Object{
				func() *apisv1alpha1.APIExport {
					apiExport := newAPIExport("foo")
					apiExport.Status.VirtualWorkspaces = []apisv1alpha1.VirtualWorkspace{{URL: "https://acme.dev"}}
					return apiExport
				}(),
			},
			initialCacheApiExports:                   []runtime.Object{newAPIExportWithShardAnnotation("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             fmt.Sprintf("%s::root|foo", apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")),
			validateFunc: func(ts *testing.T, cacheClientActions []kcptesting.Action, localClientActions []kcptesting.Action) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("update", "apiexports") {
						updateAction := action.(kcptesting.UpdateAction)
						if updateAction.GetCluster().String() != "root" {
							ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", updateAction.GetCluster())
						}
						updatedUnstructuredApiExport := updateAction.GetObject().(*unstructured.Unstructured)
						cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(updatedUnstructuredApiExport.Object, cacheApiExportFromUnstructured); err != nil {
							ts.Fatalf("failed to convert unstructured to APIExport: %v", err)
						}

						expectedApiExport := newAPIExportWithShardAnnotation("foo")
						expectedApiExport.Status.VirtualWorkspaces = []apisv1alpha1.VirtualWorkspace{{URL: "https://acme.dev"}}
						if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
							ts.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			target := &controller{shardName: "amber"}
			localApiExportIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, obj := range scenario.initialLocalApiExports {
				if err := localApiExportIndexer.Add(obj); err != nil {
					tt.Error(err)
				}
			}
			target.localApiExportLister = apislisters.NewAPIExportLister(localApiExportIndexer)
			target.cacheApiExportsIndexer = cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace})
			for _, obj := range scenario.initialCacheApiExports {
				if err := target.cacheApiExportsIndexer.Add(obj); err != nil {
					tt.Error(err)
				}
			}
			fakeCacheDynamicClient := kcpfakedynamic.NewSimpleDynamicClient(scheme, func() []runtime.Object {
				if scenario.initCacheFakeClientWithInitialApiExports {
					return scenario.initialCacheApiExports
				}
				return []runtime.Object{}
			}()...)
			target.dynamicCacheClient = fakeCacheDynamicClient
			fakeLocalDynamicClient := kcpfakedynamic.NewSimpleDynamicClient(scheme)
			target.dynamicLocalClient = fakeLocalDynamicClient
			if err := target.reconcile(context.TODO(), scenario.reconcileKey); err != nil {
				tt.Fatal(err)
			}
			if scenario.validateFunc != nil {
				scenario.validateFunc(tt, fakeCacheDynamicClient.Actions(), fakeLocalDynamicClient.Actions())
			}
		})
	}
}

func newAPIExport(name string) *apisv1alpha1.APIExport {
	return &apisv1alpha1.APIExport{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apis.kcp.dev/v1alpha1",
			Kind:       "APIExport",
		},
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{},
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "root",
			},
			Name: name,
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"lrs"},
		},
		Status: apisv1alpha1.APIExportStatus{
			IdentityHash: fmt.Sprintf("%s-identity", name),
		},
	}
}

func newAPIExportWithShardAnnotation(name string) *apisv1alpha1.APIExport {
	apiExport := newAPIExport(name)
	apiExport.Annotations["kcp.dev/shard"] = "amber"
	return apiExport
}
