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
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/invoke"
	"github.com/containernetworking/cni/pkg/types/current"
	"k8s.io/klog/v2"

	argtypes "antrea.io/antrea/pkg/agent/cniserver/types"
	crdv1a2 "antrea.io/antrea/pkg/apis/crd/v1alpha2"
	"antrea.io/antrea/pkg/ipam/poolallocator"
)

const (
	ipamAntrea = "antrea-ipam"
)

// Antrea IPAM driver would allocate IP addresses according to object IPAM annotation,
// if present. If annotation is not present, the driver will delegate functionality
// to traditional IPAM driver specified in pluginType
type AntreaIPAM struct {
	poolAllocator *poolallocator.IPPoolAllocator
	namespace     string
	podName       string
}

func generateCIDRMask(ip net.IP, prefixLength int) net.IPMask {
	ipAddrBits := 32
	if ip.To4() == nil {
		ipAddrBits = 128
	}

	return net.CIDRMask(int(prefixLength), ipAddrBits)
}

func (d *AntreaIPAM) Add(args *invoke.Args, networkConfig []byte) (*current.Result, error) {
	if d.poolAllocator == nil {
		return nil, fmt.Errorf("No IP Pool was found to allocate from for pod %s", d.podName)
	}

	ip, subnetInfo, err := d.poolAllocator.AllocateNext(crdv1a2.IPPoolUsageStateAllocated, d.podName)
	if err != nil {
		return nil, err
	}

	klog.V(2).Infof("IP %s allocated for pod %s", ip.String(), d.podName)

	result := current.Result{CNIVersion: current.ImplementedSpecVersion}
	ipConfig := &current.IPConfig{
		Address: net.IPNet{IP: ip, Mask: generateCIDRMask(ip, int(subnetInfo.PrefixLength))},
		Gateway: net.ParseIP(subnetInfo.Gateway),
	}
	result.IPs = append(result.IPs, ipConfig)
	return &result, nil
}

func (d *AntreaIPAM) Del(args *invoke.Args, networkConfig []byte) error {
	if d.poolAllocator == nil {
		return fmt.Errorf("No IP Pool was found to release IP for pod %s", d.podName)
	}

	return d.poolAllocator.ReleaseResource(d.podName)
}

func (d *AntreaIPAM) Check(args *invoke.Args, networkConfig []byte) error {
	if d.poolAllocator == nil {
		return fmt.Errorf("No IP Pool was found to check IP allocation for pod %s", d.podName)
	}

	found, err := d.poolAllocator.HasResource(d.podName)
	if err != nil {
		return err
	}

	if !found {
		return fmt.Errorf("No IP Address is associated with pod %s", d.podName)
	}

	return nil
}

func (d *AntreaIPAM) Owns(args *invoke.Args, k8sArgs *argtypes.K8sArgs, networkConfig []byte) bool {
	if antreaIPAMController == nil {
		// TODO - verify config consistency on startup
		klog.Warningf("Antrea IPAM driver failed to initialize due to inconsistent configuration. Falling back to default IPAM")
		return false
	}

	// As of today, only namespace annotation is supported
	// In future, pod annotation and stateful set annotation will be
	// supported as well
	namespace := string(k8sArgs.K8S_POD_NAMESPACE)
	klog.V(2).Infof("Inspecting IPAM annotation for namespace %s", namespace)
	poolNames, shouldOwn := antreaIPAMController.getIPPoolsByNamespace(namespace)
	if shouldOwn {
		// Only one pool is supported as of today
		// TODO - support a pool for each IP version
		ipPool := poolNames[0]
		d.namespace = namespace
		d.podName = string(k8sArgs.K8S_POD_NAME)
		klog.V(2).Infof("Pod %s in namespace %s associated with IP Pool %s", d.podName, d.namespace, ipPool)
		allocator, err := poolallocator.NewIPPoolAllocator(ipPool, antreaIPAMController.crdClient)
		if err != nil {
			klog.Warningf("Antrea IPAM driver failed to initialize IP allocator for pool %s. Falling back to default IPAM", ipPool)
			return false
		}
		d.poolAllocator = allocator
		return true
	}
	return false
}

func init() {

	// Antrea driver must come first
	if err := RegisterIPAMDriver(ipamAntrea, &AntreaIPAM{}); err != nil {
		klog.Errorf("Failed to register IPAM plugin on type %s", ipamAntrea)
	}

	// Host local plugin is fallback driver
	if err := RegisterIPAMDriver(ipamAntrea, &IPAMDelegator{pluginType: ipamHostLocal}); err != nil {
		klog.Errorf("Failed to register IPAM plugin on type %s", ipamHostLocal)
	}
}
