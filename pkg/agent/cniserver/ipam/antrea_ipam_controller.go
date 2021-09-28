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

package ipam

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	clientsetversioned "antrea.io/antrea/pkg/client/clientset/versioned"
)

const (
	controllerName = "AntreaIPAMController"
)

type AntreaIPAMController struct {
	kubeClient           clientset.Interface
	crdClient            clientsetversioned.Interface
	namespaceInformer    coreinformers.NamespaceInformer
	namespaceLister      corelisters.NamespaceLister
	namespaceAnnotations map[string][]string
}

func NewAntreaIPAMController(kubeClient clientset.Interface,
	crdClient clientsetversioned.Interface,
	informerFactory informers.SharedInformerFactory,
	resyncPeriod time.Duration) *AntreaIPAMController {

	namespaceInformer := informerFactory.Core().V1().Namespaces()
	c := AntreaIPAMController{
		kubeClient:           kubeClient,
		crdClient:            crdClient,
		namespaceInformer:    namespaceInformer,
		namespaceLister:      namespaceInformer.Lister(),
		namespaceAnnotations: make(map[string][]string),
	}

	namespaceInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    c.addNamespace,
			DeleteFunc: c.deleteNamespace,
		},
		resyncPeriod,
	)

	return &c
}

var antreaIPAMController *AntreaIPAMController

func InitializeAntreaController(kubeClient clientset.Interface, crdClient clientsetversioned.Interface, informerFactory informers.SharedInformerFactory) (*AntreaIPAMController, error) {

	antreaIPAMController = NewAntreaIPAMController(kubeClient, crdClient, informerFactory, 0)

	return antreaIPAMController, nil
}

// Run starts to watch and process Pod updates for the Node where Antrea Agent is running.
// It starts a queue and a fixed number of workers to process the objects from the queue.
func (c *AntreaIPAMController) Run(stopCh <-chan struct{}) {
	defer func() {
		klog.Infof("Shutting down %s", controllerName)
	}()

	klog.Infof("Starting %s", controllerName)
	if !cache.WaitForNamedCacheSync(controllerName, stopCh, c.namespaceInformer.Informer().HasSynced) {
		return
	}

	<-stopCh
}

func (c *AntreaIPAMController) addNamespace(obj interface{}) {
	ns, isNamespace := obj.(*corev1.Namespace)
	if !isNamespace {
		return
	}

	// TODO: add validations
	annotations := ns.Annotations[AntreaIPAMAnnotationKey]
	c.namespaceAnnotations[ns.Name] = strings.Split(annotations, ";")
}

func (c *AntreaIPAMController) deleteNamespace(obj interface{}) {
	ns, isNamespace := obj.(*corev1.Namespace)
	if !isNamespace {
		return
	}

	// TODO: add validations
	c.namespaceAnnotations[ns.Name] = nil
}

func (c *AntreaIPAMController) getIPPoolsByNamespace(namespace string) ([]string, bool) {
	pools, exists := c.namespaceAnnotations[namespace]
	return pools, exists
}
