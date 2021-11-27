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
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"antrea.io/antrea/pkg/client/clientset/versioned"
	crdinformers "antrea.io/antrea/pkg/client/informers/externalversions/crd/v1alpha2"
	crdlisters "antrea.io/antrea/pkg/client/listers/crd/v1alpha2"
	"antrea.io/antrea/pkg/ipam/poolallocator"
)

const (
	controllerName = "AntreaIPAMController"

	// TODO: currently these constants are duplicated, move to a shared place
	AntreaIPAMAnnotationKey       = "ipam.antrea.io/ippools"
	AntreaIPAMAnnotationDelimiter = ","

	minRetryDelay = 5 * time.Second
	maxRetryDelay = 300 * time.Second
)

// AntreaIPAMController is responsible for IP address cleanup
// for StatefulSet objects.
// In future, it will also be responsible for pre-allocation of
// continuous IP range for StatefulSets that do not have dedicated
// IP Pool annotation.
type AntreaIPAMController struct {
	// crdClient is the clientset for CRD API group.
	crdClient versioned.Interface

	// Pool cleanup events triggered by StatefulSet delete
	statefulSetCleanupQueue workqueue.RateLimitingInterface
	statefulSetMap          sync.Map

	// follow changes for Namespace objects
	namespaceLister       corelisters.NamespaceLister
	namespaceListerSynced cache.InformerSynced

	// follow changes for StatefulSet objects
	statefulSetInformer     appsinformers.StatefulSetInformer
	statefulSetListerSynced cache.InformerSynced

	// follow changes for Pod objects
	podLister       corelisters.PodLister
	podListerSynced cache.InformerSynced

	// follow changes for IP Pool objects
	ipPoolLister       crdlisters.IPPoolLister
	ipPoolListerSynced cache.InformerSynced
}

func NewAntreaIPAMController(crdClient versioned.Interface,
	statefulSetInformer appsinformers.StatefulSetInformer,
	namespaceInformer coreinformers.NamespaceInformer,
	podInformer coreinformers.PodInformer,
	ipPoolInformer crdinformers.IPPoolInformer) *AntreaIPAMController {
	c := &AntreaIPAMController{
		crdClient:               crdClient,
		statefulSetCleanupQueue: workqueue.NewNamedRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(minRetryDelay, maxRetryDelay), "statefulSetCleanup"),
		statefulSetMap:          sync.Map{},
		namespaceLister:         namespaceInformer.Lister(),
		namespaceListerSynced:   namespaceInformer.Informer().HasSynced,
		podLister:               podInformer.Lister(),
		podListerSynced:         podInformer.Informer().HasSynced,
		statefulSetInformer:     statefulSetInformer,
		statefulSetListerSynced: statefulSetInformer.Informer().HasSynced,
		ipPoolLister:            ipPoolInformer.Lister(),
		ipPoolListerSynced:      ipPoolInformer.Informer().HasSynced,
	}

	// Add handlers for Stateful Set events.
	klog.V(2).InfoS("Subscribing for StatefulSet notifications", "controller", controllerName)
	statefulSetInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			DeleteFunc: c.enqueueStatefulSetDeleteEvent,
		},
	)
	return c
}

// Enqueue the STatefulSet delete notification to process by the worker
func (c *AntreaIPAMController) enqueueStatefulSetDeleteEvent(obj interface{}) {
	ss := obj.(*appsv1.StatefulSet)
	klog.V(2).InfoS("Delete notification", "StatefulSet", ss.Name)

	key := ss.GetUID()
	c.statefulSetMap.Store(key, ss)
	c.statefulSetCleanupQueue.Add(key)
}

// Find IP Pools annotated to StatefulSet via direct annotation or namespace annotation
func (c *AntreaIPAMController) getIPPoolsByStatefulSet(ss *appsv1.StatefulSet) []string {

	// Inspect pool annotation for the Pods
	// In order to avoid extra API call in IPAM driver, IPAM annotations are defined
	// on Pods rather than on StatefulSet
	selector := map[string]string{"app": ss.Name}
	podList, err := c.podLister.Pods(ss.Namespace).List(labels.SelectorFromSet(labels.Set(selector)))

	if err != nil {
		klog.Errorf("Failed to list Pods for StatefulSet %s/%s", ss.Namespace, ss.Name)
		return nil
	}

	for _, pod := range podList {
		annotations, exists := pod.Annotations[AntreaIPAMAnnotationKey]
		if exists {
			// Stateful Set Pod is annotated with dedicated IP pool
			return strings.Split(annotations, AntreaIPAMAnnotationDelimiter)
		}
	}

	// Inspect Namespace
	namespace, err := c.namespaceLister.Get(ss.Namespace)
	if err != nil {
		// Should never happen
		klog.Errorf("Namespace %s not found for StatefulSet %s", ss.Namespace, ss.Name)
		return nil
	}

	annotations, exists := namespace.Annotations[AntreaIPAMAnnotationKey]
	if exists {
		return strings.Split(annotations, AntreaIPAMAnnotationDelimiter)
	}

	return nil

}

// Look for an IP Pool associated with this StatefulSet, either a dedicated one or
// annotated to the namespace. If IPPool is found, this routine will clear all IP
// addresses that might be reserved for the pool.
func (c *AntreaIPAMController) cleanIPPoolForStatefulSet(ss *appsv1.StatefulSet) error {
	klog.InfoS("Processing delete notification", "StatefulSet", ss.Name)
	poolNames := c.getIPPoolsByStatefulSet(ss)
	if len(poolNames) == 0 {
		// no Antrea IPAM annotation for this StatefulSet
		return nil
	}

	klog.InfoS("Clearing reserved IP addresses", "StatefulSet", ss.Name, "IP Pools", poolNames)
	// Only single pool is supported for now.
	// TODO: Support dual stack.
	poolName := poolNames[0]

	allocator, err := poolallocator.NewIPPoolAllocator(poolName, c.crdClient, c.ipPoolLister)
	if err != nil {
		// This is not a transient error - log and forget
		klog.Errorf("Failed to find IP Pool %s", poolName)
		return nil
	}

	err = allocator.ReleaseStatefulSet(ss.Namespace, ss.Name)
	if err != nil {
		// This can be a transient error - worker will retry
		klog.Errorf("Failed to clean IP allocations for StatefulSet %s in Pool %s", ss.Name, poolName)
		return err

	}

	return nil
}

func (c *AntreaIPAMController) statefulSetWorker() {
	for c.processNextStatefulSetWorkItem() {
	}
}

func (c *AntreaIPAMController) processNextStatefulSetWorkItem() bool {
	key, quit := c.statefulSetCleanupQueue.Get()
	if quit {
		return false
	}

	defer c.statefulSetCleanupQueue.Done(key)
	ss, ok := c.statefulSetMap.Load(key)
	if !ok {
		// Object not found in map - should never happen
		klog.Errorf("Failed to locate StatefulSet for key %s", key)
		c.statefulSetCleanupQueue.Forget(key)
		return true
	}
	err := c.cleanIPPoolForStatefulSet(ss.(*appsv1.StatefulSet))

	if err != nil {
		// Put the item back on the workqueue to handle any transient errors.
		c.statefulSetCleanupQueue.AddRateLimited(key)
		klog.Errorf("Failed to clean IP Pool for StatefulSet %s: %v", key, err)
		return true
	}

	c.statefulSetCleanupQueue.Forget(key)
	c.statefulSetMap.Delete(key)
	return true
}

// Run begins watching and syncing of a AntreaIPAMController.
func (c *AntreaIPAMController) Run(stopCh <-chan struct{}) {

	defer c.statefulSetCleanupQueue.ShutDown()

	klog.InfoS("Starting", "controller", controllerName)
	defer klog.InfoS("Shutting down", "controller", controllerName)

	cacheSyncs := []cache.InformerSynced{c.statefulSetListerSynced, c.namespaceListerSynced, c.podListerSynced, c.ipPoolListerSynced}
	if !cache.WaitForNamedCacheSync(controllerName, stopCh, cacheSyncs...) {
		return
	}

	go wait.Until(c.statefulSetWorker, time.Second, stopCh)

	<-stopCh
}
