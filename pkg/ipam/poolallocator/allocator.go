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

package poolallocator

import (
	"fmt"
	"net"

	crdv1alpha2 "antrea.io/antrea/pkg/apis/crd/v1alpha2"
	crdlister "antrea.io/antrea/pkg/client/listers/crd/v1alpha2"

	"antrea.io/antrea/pkg/ipam/ipallocator"
)

// IPPoolAllocator is responsible for allocating IPs from IP set defined in IPPool CRD.
// The will update CRD usage accordingly.
type IPPoolAllocator struct {
	// Name of IP Pool custom resource
	ipPoolName string

	// crd client to access the pool
	crdClient crdclientset.Interface
}

// NewIPPoolAllocator creates an IPPoolAllocator based on the provided IP pool.
func NewIPPoolAllocator(poolName string, client crdclientset.Interface) (*IPPoolAllocator, error) {

	allocator := &IPPoolAllocator{
		ipPoolName: poolName,
		crdClient:  client,
	}
	return allocator, nil
}

// initAllocatorList reads IP Pool status and initializes a list of allocators based on
// IP Pool spec and state of allocation recorded in the status
func (a *IPPoolAllocator) initAllocators(ipPool crdv1alpha2.IPPool) (ipallocator.MultiIPAllocator, error) {

	var allocators MultiIPAllocator

	// Initalize a list of IP allocators based on pool spec
	for _, ipRange := range ippool.Spec {
		if len(ipRange.CIDR) > 0 {
			allocators = append(allocators, ipallocator.NewCIDRAllocator(ipRange.CIDR))
		} else {
			allocators = append(allocators, ipallocator.NewIPRangeAllocator(ipRange.Start, ipRange.End))
		}
	}

	// Mark allocated IPs from pool status as unavailable
	for _, ip := range ippool.Status.Usage {
		err := allocators.AllocateIP(ip.ipAddress)
		if err != nil {
			// TODO - fix state if possible
			return allocators, fmt.Errorf("Inconsistent state for IP Pool %s with IP %s", ipPool.Name, ip.ipAddress)
		}
	}

	return allocators, nil
}

func (a *IPPoolAllocator) appendPoolUsage(ipPool *crdv1alpha2.IPPool, ip net.IP, state string, resource string) error {
	usageEntry := v1alpha2.IPPoolUsage{
		IPAddress: ip,
		State:     state,
		Resource:  resource,
	}

	newPool = ipPool.DeepCopy()
	newPool.Status.Usage = append(ipPool.Status.Usage, usageEntry)
	_, err := a.crdClient.CrdV1alpha2().IPPools().UpdateStatus(context.TODO(), newPool, v1.UpdateOptions{})
	if err != nil {
		klog.Warningf("IP Pool %s update failed: %+v", ipPool.Name, err)
		return err
	}
	klog.Infof("IP Pool update succesful %s: %+v", ipPool.Name, newPool.Status)
	return nil

}

func (a *IPPoolAllocator) removePoolUsage(ipPool *crdv1alpha2.IPPool, ip net.IP) error {

	for i, entry := range ipPool.Status.Usage {
		if entry.ipAddress == ip {
			break
		}
	}

	if i == len(ipPool.Status.Usage) {
		return fmt.Errorf("IP address %s was not allocated from IP pool %s", ip, ipPool.Name)
	}

	newPool = ipPool.DeepCopy()
	newPool.Status.Usage = append(ipPool.Status.Usage[:i], ipPool.Status.Usage[si1:]...)

	_, err := a.crdClient.CrdV1alpha2().IPPools().UpdateStatus(context.TODO(), newPool, v1.UpdateOptions{})
	if err != nil {
		klog.Warningf("IP Pool %s update failed: %+v", ipPool.Name, err)
		return err
	}
	klog.Infof("IP Pool update succesful %s: %+v", ipPool.Name, newPool.Status)
	return nil

}

// AllocateIP allocates the specified IP. It returns error if the IP is not in the range or already
// allocated, or in case CRD failed to update its state.
// In case of success, IP pool CRD status is updated with allocated IP/state/resource.
// AllocateIP returns subnet detailes for the requested IP, as defined in IP pool spec.
func (a *IPPoolAllocator) AllocateIP(ip net.IP, state string, resource string) (crdv1alpha2.SubnetSpec, error) {
	var subnetSpec crdv1alpha2.SubnetSpec
	// Retry on CRD update conflict which is caused by multiple agents updating a pool at same time.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ipPool, err := a.crdClient.CrdV1alpha2().IPPools().Get(poolName)

		if err != nil {
			return err
		}

		allocators, err := a.initAllocators(&ipPool)
		if err != nil {
			return err
		}

		for i, allocator := range allocators {
			if allocator.Has(ip) {
				err := allocator.AllocateIP(ip)
				if err != nil {
					return err
				}
				break
			}
		}

		if i == len(allocators) {
			// Failed to find matching range
			return fmt.Errorf("IP %v does not belong to IP pool %s", ip, a.poolName)
		}

		err = a.updatedUsage(ipPool, ip, status, resource)

		subnetSpec = ipPool.Spec[i].SubnetSpec.DeepCopy()
		return err
	})

	if err != nil {
		klog.Errorf("Failed to allocate IP address %s from pool %s: %+v", ip, a.poolName, err)
	}
	return subnetSpec, err
}

// AllocateNext allocates the next available IP. It returns error if pool is exausted,
// or in case CRD failed to update its state.
// In case of success, IP pool CRD status is updated with allocated IP/state/resource.
// AllocateIP returns subnet detailes for the requested IP, as defined in IP pool spec.
func (a *IPPoolAllocator) AllocateNext(state string, resource string) (net.IP, crdv1alpha2.SubnetSpec, error) {
	var subnetSpec crdv1alpha2.SubnetSpec
	var ip net.IP
	// Retry on CRD update conflict which is caused by multiple agents updating a pool at same time.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ipPool, err := a.crdClient.CrdV1alpha2().IPPools().Get(poolName)

		if err != nil {
			return err
		}

		allocators, err := a.initAllocators(&ipPool)
		if err != nil {
			return err
		}

		for i, allocator := range allocators {
			ip, err = allocator.AllocateNext()
			if err == nil {
				// succesful allocation
				break
			}
		}

		if i == len(allocators) {
			// Failed to find matching range
			return fmt.Errorf("Failed to allocatoe IP: Pool %s is exausted", a.poolName)
		}

		err = a.appendPoolUsage(ipPool, ip, status, resource)

		result = ipPool.Spec[i].SubnetSpec.DeepCopy()
		return err
	})

	if err != nil {
		klog.Errorf("Failed to allocate from pool %s: %+v", a.poolName, err)
	}
	return ip, subnetSpec, err
}

// Release releases the provided IP. It returns error if the IP is not in the range or not allocated,
// or in case CRD failed to update its state.
// In case of success, IP pool CRD status is updated with released IP/state/resource.
func (a *IPPoolAllocator) Release(ip net.IP) error {

	// Retry on CRD update conflict which is caused by multiple agents updating a pool at same time.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ipPool, err := a.crdClient.CrdV1alpha2().IPPools().Get(poolName)

		if err != nil {
			return err
		}

		allocators, err := a.initAllocators(&ipPool)
		if err != nil {
			return err
		}

		err = allocators.Release(ip)

		if err != nil {
			// Failed to find matching range
			return fmt.Errorf("IP %v does not belong to IP pool %s", ip, a.poolName)
		}

		return a.removePoolUsage(ipPool, ip)
	})

	if err != nil {
		klog.Errorf("Failed to release IP address %s from pool %s: %+v", ip, a.poolName, err)
	}
	return err
}
