// Copyright 2021 Antrea Authors
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

// Package networkpolicy provides AntreaIPAMController implementation to manage
// and synchronize the GroupMembers and Namespaces affected by Network Policies and enforce
// their rules.
package ipam

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"

	crdv1a2 "antrea.io/antrea/pkg/apis/crd/v1alpha2"
	fakecrd "antrea.io/antrea/pkg/client/clientset/versioned/fake"
	crdinformers "antrea.io/antrea/pkg/client/informers/externalversions"
	listers "antrea.io/antrea/pkg/client/listers/crd/v1alpha2"
	"antrea.io/antrea/pkg/ipam/poolallocator"
)

var (
	testPool     = "test-pool"
	testWithPool = "test-pool"
	testNoPool   = "test-no-pool"
)

// StatefulSet objects are not defined here, since IPAM annotations
// are configured on Pods which belong to the StatefulSet via "app" label.
func initTestClients() (*fake.Clientset, *fakecrd.Clientset) {
	ipRange := crdv1a2.IPRange{
		Start: "10.2.2.100",
		End:   "10.2.2.200",
	}

	subnetInfo := crdv1a2.SubnetInfo{
		Gateway:      "10.2.2.1",
		PrefixLength: 24,
	}

	subnetRange := crdv1a2.SubnetIPRange{IPRange: ipRange,
		SubnetInfo: subnetInfo}

	crdClient := fakecrd.NewSimpleClientset(
		&crdv1a2.IPPool{
			ObjectMeta: metav1.ObjectMeta{Name: testPool},
			Spec: crdv1a2.IPPoolSpec{
				IPRanges: []crdv1a2.SubnetIPRange{subnetRange},
			},
		},
	)

	k8sClient := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        testWithPool,
				Annotations: map[string]string{AntreaIPAMAnnotationKey: testPool},
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNoPool,
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testNoPool,
				Namespace: testWithPool,
				Labels:    map[string]string{"app": testNoPool},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{AntreaIPAMAnnotationKey: testPool},
				Labels:      map[string]string{"app": testWithPool},
				Namespace:   testNoPool,
			},
		},
	)

	return k8sClient, crdClient
}

func verifyPoolAllocatedSize(t *testing.T, poolLister listers.IPPoolLister, size int) {

	err := wait.PollImmediate(100*time.Millisecond, 10*time.Second, func() (bool, error) {
		pool, err := poolLister.Get(testPool)
		if err != nil {
			return false, nil
		}
		if len(pool.Status.IPAddresses) == size {
			return true, nil
		}

		return false, nil
	})

	require.NoError(t, err)
}

// This test verifies release of reserved IPs for dedicated IP pool annotation,
// as well as namespace-based IP Pool annotation.
func TestReleaseStatefulSet(t *testing.T) {
	stopCh := make(chan struct{})

	k8sClient, crdClient := initTestClients()

	informerFactory := informers.NewSharedInformerFactory(k8sClient, 0)

	namespaceInformer := informerFactory.Core().V1().Namespaces()
	podInformer := informerFactory.Core().V1().Pods()
	statefulSetInformer := informerFactory.Apps().V1().StatefulSets()

	crdInformerFactory := crdinformers.NewSharedInformerFactory(crdClient, 0)
	poolInformer := crdInformerFactory.Crd().V1alpha2().IPPools()
	poolLister := poolInformer.Lister()

	controller := NewAntreaIPAMController(crdClient, statefulSetInformer, namespaceInformer, podInformer, poolInformer)
	require.NotNil(t, controller)
	informerFactory.Start(stopCh)
	crdInformerFactory.Start(stopCh)

	go controller.Run(stopCh)

	var allocator *poolallocator.IPPoolAllocator
	var err error
	// Wait until pool propagates to the informer
	pollErr := wait.PollImmediate(100*time.Millisecond, 1*time.Second, func() (bool, error) {
		allocator, err = poolallocator.NewIPPoolAllocator(testPool, crdClient, poolLister)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, pollErr)

	informerFactory.Start(stopCh)
	verifyPoolAllocatedSize(t, poolLister, 0)

	// Allocate StatefulSet with dedicated IP Pool
	err = allocator.AllocateStatefulSet(testNoPool, testWithPool, 5)
	require.NoError(t, err, "Failed to reserve IPs for StatefulSet")
	verifyPoolAllocatedSize(t, poolLister, 5)

	// Allocate StatefulSet with namespace-based IP Pool annotation
	err = allocator.AllocateStatefulSet(testWithPool, testNoPool, 3)
	require.NoError(t, err, "Failed to reserve IPs for StatefulSet")
	verifyPoolAllocatedSize(t, poolLister, 8)

	// Delete StatefulSet with namespace-based IP Pool annotation
	controller.enqueueStatefulSetDeleteEvent(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testNoPool,
			Namespace: testWithPool,
		}})

	// Verify delete event was handled by the controller
	verifyPoolAllocatedSize(t, poolLister, 5)

	// Delete StatefulSet with dedicated IP Pool
	controller.enqueueStatefulSetDeleteEvent(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWithPool,
			Namespace: testNoPool,
		}})

	// Verify delete event was handled by the controller
	verifyPoolAllocatedSize(t, poolLister, 0)
}
