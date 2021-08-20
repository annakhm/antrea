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
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	crdv1a2 "antrea.io/antrea/pkg/apis/crd/v1alpha2"
	fakeversioned "antrea.io/antrea/pkg/client/clientset/versioned/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func newIPPoolAllocator(poolName string, initObjects []runtime.Object) *IPPoolAllocator {
	crdClient := fakeversioned.NewSimpleClientset(initObjects...)
	allocator, _ := NewIPPoolAllocator(poolName, crdClient)
	return allocator
}

func TestAllocateIP(t *testing.T) {
	poolName := "fakePool"
	ipRange := crdv1a2.IPRange{
		Start: "10.2.2.100",
		End:   "10.2.2.120",
	}
	subnetSpec := crdv1a2.SubnetInfo{
		Gateway:      "10.2.2.1",
		PrefixLength: 24,
	}
	subnetRange := crdv1a2.SubnetIPRange{IPRange: ipRange,
		SubnetInfo: subnetSpec}

	pool := crdv1a2.IPPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName},
		Spec:       crdv1a2.IPPoolSpec{IPRanges: []crdv1a2.SubnetIPRange{subnetRange}},
	}

	allocator := newIPPoolAllocator(poolName, []runtime.Object{&pool})

	// Allocate specific IP from the range
	returnSpec, err := allocator.AllocateIP(net.ParseIP("10.2.2.101"), crdv1a2.IPPoolUsageStateAllocated, "fakePod")
	assert.Equal(t, subnetSpec, returnSpec)
	require.NoError(t, err)

	// Validate IP outside the range is not allocated
	returnSpec, err = allocator.AllocateIP(net.ParseIP("10.2.2.121"), crdv1a2.IPPoolUsageStateAllocated, "fakePod")
	require.Error(t, err)

	// Make sure IP allocated above is not allocated again
	for _, expectedIP := range []string{"10.2.2.100", "10.2.2.102"} {
		ip, returnSpec, err := allocator.AllocateNext(crdv1a2.IPPoolUsageStateAllocated, "fakePod")
		require.NoError(t, err)
		assert.Equal(t, net.ParseIP(expectedIP), ip)
		assert.Equal(t, subnetSpec, returnSpec)
	}
}

func TestAllocateNext(t *testing.T) {
	poolName := "fakePool"
	ipRange := crdv1a2.IPRange{
		Start: "10.2.2.100",
		End:   "10.2.2.120",
	}
	subnetSpec := crdv1a2.SubnetInfo{
		Gateway:      "10.2.2.1",
		PrefixLength: 24,
	}
	subnetRange := crdv1a2.SubnetIPRange{IPRange: ipRange,
		SubnetInfo: subnetSpec}

	pool := crdv1a2.IPPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName},
		Spec:       crdv1a2.IPPoolSpec{IPRanges: []crdv1a2.SubnetIPRange{subnetRange}},
	}

	allocator := newIPPoolAllocator(poolName, []runtime.Object{&pool})

	for _, expectedIP := range []string{"10.2.2.100", "10.2.2.101"} {
		ip, returnSpec, err := allocator.AllocateNext(crdv1a2.IPPoolUsageStateAllocated, "fakePod")
		require.NoError(t, err)
		assert.Equal(t, net.ParseIP(expectedIP), ip)
		assert.Equal(t, subnetSpec, returnSpec)
	}
}

func TestAllocateNextMultiRange(t *testing.T) {
	poolName := "fakePool"
	ipRange1 := crdv1a2.IPRange{
		Start: "10.2.2.100",
		End:   "10.2.2.101",
	}
	ipRange2 := crdv1a2.IPRange{CIDR: "10.2.2.0/28"}
	subnetSpec := crdv1a2.SubnetInfo{
		Gateway:      "10.2.2.1",
		PrefixLength: 24,
	}
	subnetRange1 := crdv1a2.SubnetIPRange{IPRange: ipRange1,
		SubnetInfo: subnetSpec}
	subnetRange2 := crdv1a2.SubnetIPRange{IPRange: ipRange2,
		SubnetInfo: subnetSpec}

	pool := crdv1a2.IPPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName},
		Spec: crdv1a2.IPPoolSpec{
			IPRanges: []crdv1a2.SubnetIPRange{subnetRange1, subnetRange2}},
	}

	allocator := newIPPoolAllocator(poolName, []runtime.Object{&pool})

	// Allocate the 2 available IPs from first range then switch to second range
	for _, expectedIP := range []string{"10.2.2.100", "10.2.2.101", "10.2.2.1", "10.2.2.2"} {
		ip, returnSpec, err := allocator.AllocateNext(crdv1a2.IPPoolUsageStateAllocated, "fakePod")
		require.NoError(t, err)
		assert.Equal(t, net.ParseIP(expectedIP), ip)
		assert.Equal(t, subnetSpec, returnSpec)
	}
}

func TestAllocateReleaseSequence(t *testing.T) {
	poolName := "fakePool"
	ipRange1 := crdv1a2.IPRange{
		Start: "2001::1000",
		End:   "2001::1000",
	}
	ipRange2 := crdv1a2.IPRange{CIDR: "2001::0/124"}
	subnetSpec := crdv1a2.SubnetInfo{
		Gateway:      "2001::1",
		PrefixLength: 64,
	}
	subnetRange1 := crdv1a2.SubnetIPRange{IPRange: ipRange1,
		SubnetInfo: subnetSpec}
	subnetRange2 := crdv1a2.SubnetIPRange{IPRange: ipRange2,
		SubnetInfo: subnetSpec}

	pool := crdv1a2.IPPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName},
		Spec: crdv1a2.IPPoolSpec{
			IPRanges: []crdv1a2.SubnetIPRange{subnetRange1, subnetRange2}},
	}

	allocator := newIPPoolAllocator(poolName, []runtime.Object{&pool})

	testAllocate := func(expectedIPs []string) {
		for _, expectedIP := range expectedIPs {
			ip, returnSpec, err := allocator.AllocateNext(crdv1a2.IPPoolUsageStateAllocated, "fakePod")
			require.NoError(t, err)
			assert.Equal(t, net.ParseIP(expectedIP), ip)
			assert.Equal(t, subnetSpec, returnSpec)
		}
	}

	// Allocate the single available IPs from first range then 3 IPs from second range
	testAllocate([]string{"2001::1000", "2001::1", "2001::2", "2001::3"})

	// Release first IP from first range and middle IP from second range
	for _, ipToRelease := range []string{"2001::1000", "2001::2"} {
		err := allocator.Release(net.ParseIP(ipToRelease))
		require.NoError(t, err)
	}

	// Allocate next IPs to validate released IPs are reallocate again
	testAllocate([]string{"2001::1000", "2001::2", "2001::4"})
}
