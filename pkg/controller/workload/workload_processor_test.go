/*
 * Copyright The Kmesh Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at:
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package workload

import (
	"net/netip"
	"testing"

	service_discovery_v3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"istio.io/istio/pilot/pkg/util/protoconv"
	"istio.io/istio/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/rand"

	"kmesh.net/kmesh/api/v2/workloadapi"
	"kmesh.net/kmesh/daemon/options"
	"kmesh.net/kmesh/pkg/bpf"
	"kmesh.net/kmesh/pkg/constants"
	"kmesh.net/kmesh/pkg/controller/workload/bpfcache"
	"kmesh.net/kmesh/pkg/controller/workload/cache"
	"kmesh.net/kmesh/pkg/nets"
	"kmesh.net/kmesh/pkg/utils/test"
)

func Test_handleWorkload(t *testing.T) {
	workloadMap := bpfcache.NewFakeWorkloadMap(t)
	defer bpfcache.CleanupFakeWorkloadMap(workloadMap)

	p := newProcessor(workloadMap)

	var (
		ek bpfcache.EndpointKey
		ev bpfcache.EndpointValue
	)

	// 1. add related service
	fakeSvc := createFakeService("testsvc", "10.240.10.1", "10.240.10.2")
	_ = p.handleService(fakeSvc)

	// 2. add workload
	wl := createTestWorkloadWithService(true)
	err := p.handleWorkload(wl)
	assert.NoError(t, err)

	workloadID := checkFrontEndMap(t, wl.Addresses[0], p)
	checkBackendMap(t, p, workloadID, wl)

	// 2.1 check front end map contains service
	svcID := checkFrontEndMap(t, fakeSvc.Addresses[0].Address, p)

	// 2.2 check service map contains service
	checkServiceMap(t, p, svcID, fakeSvc, 1)

	// 2.3 check endpoint map now contains the workloads
	ek.BackendIndex = 1
	ek.ServiceId = svcID
	err = p.bpf.EndpointLookup(&ek, &ev)
	assert.NoError(t, err)
	assert.Equal(t, ev.BackendUid, workloadID)

	// 3. add another workload with service
	workload2 := createFakeWorkload("1.2.3.5", workloadapi.NetworkMode_STANDARD)
	err = p.handleWorkload(workload2)
	assert.NoError(t, err)

	// 3.1 check endpoint map now contains the new workloads
	workload2ID := checkFrontEndMap(t, workload2.Addresses[0], p)
	ek.BackendIndex = 2
	ek.ServiceId = svcID
	err = p.bpf.EndpointLookup(&ek, &ev)
	assert.NoError(t, err)
	assert.Equal(t, ev.BackendUid, workload2ID)

	// 3.2 check service map contains service
	checkServiceMap(t, p, svcID, fakeSvc, 2)

	// 4 modify workload2 attribute not related with services
	workload2.Waypoint = &workloadapi.GatewayAddress{
		Destination: &workloadapi.GatewayAddress_Address{
			Address: &workloadapi.NetworkAddress{
				Address: netip.MustParseAddr("10.10.10.10").AsSlice(),
			},
		},
		HboneMtlsPort: 15008,
	}

	err = p.handleWorkload(workload2)
	assert.NoError(t, err)
	checkBackendMap(t, p, workload2ID, workload2)

	// 4.1 check endpoint map now contains the new workloads
	workload2ID = checkFrontEndMap(t, workload2.Addresses[0], p)
	ek.BackendIndex = 2
	ek.ServiceId = svcID
	err = p.bpf.EndpointLookup(&ek, &ev)
	assert.NoError(t, err)
	assert.Equal(t, ev.BackendUid, workload2ID)

	// 4.2 check service map contains service
	checkServiceMap(t, p, svcID, fakeSvc, 2)

	// 4.3 check backend map contains waypoint
	checkBackendMap(t, p, workload2ID, workload2)

	// 5 update workload to remove the bound services
	wl3 := proto.Clone(wl).(*workloadapi.Workload)
	wl3.Services = nil
	err = p.handleWorkload(wl3)
	assert.NoError(t, err)

	// 5.1 check service map
	checkServiceMap(t, p, svcID, fakeSvc, 1)

	// 5.2 check endpoint map
	ek.BackendIndex = 1
	ek.ServiceId = svcID
	err = p.bpf.EndpointLookup(&ek, &ev)
	assert.NoError(t, err)
	assert.Equal(t, workload2ID, ev.BackendUid)

	// 6. add namespace scoped waypoint service
	wpSvc := createFakeService("waypoint", "10.240.10.5", "10.240.10.5")
	_ = p.handleService(wpSvc)
	assert.Nil(t, wpSvc.Waypoint)
	// 6.1 check front end map contains service
	svcID = checkFrontEndMap(t, wpSvc.Addresses[0].Address, p)
	// 6.2 check service map contains service, but no waypoint address
	checkServiceMap(t, p, svcID, wpSvc, 0)

	// 7. delete service
	p.handleRemovedAddresses([]string{fakeSvc.ResourceName()})
	checkNotExistInFrontEndMap(t, fakeSvc.Addresses[0].Address, p)

	hashNameClean(p)
}

func Test_hostnameNetworkMode(t *testing.T) {
	workloadMap := bpfcache.NewFakeWorkloadMap(t)
	p := newProcessor(workloadMap)
	workload := createFakeWorkload("1.2.3.4", workloadapi.NetworkMode_STANDARD)
	workloadWithoutService := createFakeWorkload("1.2.3.5", workloadapi.NetworkMode_STANDARD)
	workloadWithoutService.Services = nil
	workloadHostname := createFakeWorkload("1.2.3.6", workloadapi.NetworkMode_HOST_NETWORK)

	p.handleWorkload(workload)
	p.handleWorkload(workloadWithoutService)
	p.handleWorkload(workloadHostname)

	// Check Workload Cache
	checkWorkloadCache(t, p, workload)
	checkWorkloadCache(t, p, workloadWithoutService)
	checkWorkloadCache(t, p, workloadHostname)

	// Check Frontend Map
	checkFrontEndMapWithNetworkMode(t, workload.Addresses[0], p, workload.NetworkMode)
	checkFrontEndMapWithNetworkMode(t, workloadWithoutService.Addresses[0], p, workloadWithoutService.NetworkMode)
	checkFrontEndMapWithNetworkMode(t, workloadHostname.Addresses[0], p, workloadHostname.NetworkMode)
}

func checkWorkloadCache(t *testing.T, p *Processor, workload *workloadapi.Workload) {
	ip := workload.Addresses[0]
	address := cache.NetworkAddress{
		Network: workload.Network,
	}
	address.Address, _ = netip.AddrFromSlice(ip)
	// host network mode is not managed by kmesh
	if workload.NetworkMode == workloadapi.NetworkMode_HOST_NETWORK {
		assert.Nil(t, p.WorkloadCache.GetWorkloadByAddr(address))
	} else {
		assert.NotNil(t, p.WorkloadCache.GetWorkloadByAddr(address))
	}
	// We store pods by their uids regardless of their network mode
	assert.NotNil(t, p.WorkloadCache.GetWorkloadByUid(workload.Uid))
}

func checkServiceMap(t *testing.T, p *Processor, svcId uint32, fakeSvc *workloadapi.Service, endpointCount uint32) {
	var sv bpfcache.ServiceValue
	err := p.bpf.ServiceLookup(&bpfcache.ServiceKey{ServiceId: svcId}, &sv)
	assert.NoError(t, err)
	assert.Equal(t, endpointCount, sv.EndpointCount)
	waypointAddr := fakeSvc.GetWaypoint().GetAddress().GetAddress()
	if waypointAddr != nil {
		assert.Equal(t, test.EqualIp(sv.WaypointAddr, waypointAddr), true)
	}

	assert.Equal(t, sv.WaypointPort, nets.ConvertPortToBigEndian(fakeSvc.Waypoint.GetHboneMtlsPort()))
}

func checkEndpointMap(t *testing.T, p *Processor, fakeSvc *workloadapi.Service, backendUid []uint32) {
	endpoints := p.bpf.GetAllEndpointsForService(p.hashName.Hash(fakeSvc.ResourceName()))
	assert.Equal(t, len(endpoints), len(backendUid))

	all := sets.New[uint32](backendUid...)
	for _, endpoint := range endpoints {
		if !all.Contains(endpoint.BackendUid) {
			t.Fatalf("endpoint %v, unexpected", endpoint.BackendUid)
		}
	}
}

func checkBackendMap(t *testing.T, p *Processor, workloadID uint32, wl *workloadapi.Workload) {
	var bv bpfcache.BackendValue
	err := p.bpf.BackendLookup(&bpfcache.BackendKey{BackendUid: workloadID}, &bv)
	assert.NoError(t, err)
	assert.Equal(t, test.EqualIp(bv.Ip, wl.Addresses[0]), true)
	waypointAddr := wl.GetWaypoint().GetAddress().GetAddress()
	if waypointAddr != nil {
		assert.Equal(t, test.EqualIp(bv.WaypointAddr, waypointAddr), true)
	}
	assert.Equal(t, bv.WaypointPort, nets.ConvertPortToBigEndian(wl.GetWaypoint().GetHboneMtlsPort()))
}

func checkFrontEndMapWithNetworkMode(t *testing.T, ip []byte, p *Processor, networkMode workloadapi.NetworkMode) (upstreamId uint32) {
	var fk bpfcache.FrontendKey
	var fv bpfcache.FrontendValue
	nets.CopyIpByteFromSlice(&fk.Ip, ip)
	err := p.bpf.FrontendLookup(&fk, &fv)
	if networkMode != workloadapi.NetworkMode_HOST_NETWORK {
		assert.NoError(t, err)
		upstreamId = fv.UpstreamId
	} else {
		assert.Error(t, err)
	}
	return
}

func checkFrontEndMap(t *testing.T, ip []byte, p *Processor) (upstreamId uint32) {
	var fk bpfcache.FrontendKey
	var fv bpfcache.FrontendValue
	nets.CopyIpByteFromSlice(&fk.Ip, ip)
	err := p.bpf.FrontendLookup(&fk, &fv)
	assert.NoError(t, err)
	upstreamId = fv.UpstreamId
	return
}

func checkNotExistInFrontEndMap(t *testing.T, ip []byte, p *Processor) {
	var fk bpfcache.FrontendKey
	var fv bpfcache.FrontendValue
	nets.CopyIpByteFromSlice(&fk.Ip, ip)
	err := p.bpf.FrontendLookup(&fk, &fv)
	if err == nil {
		t.Fatalf("expected not exist error")
	}
}

func BenchmarkAddNewServicesWithWorkload(b *testing.B) {
	t := &testing.T{}
	config := options.BpfConfig{
		Mode:        constants.WorkloadMode,
		BpfFsPath:   "/sys/fs/bpf",
		Cgroup2Path: "/mnt/kmesh_cgroup2",
		EnableMda:   false,
	}
	cleanup, bpfLoader := test.InitBpfMap(t, config)
	b.Cleanup(cleanup)

	workloadController := NewController(bpfLoader.GetBpfKmeshWorkload())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		workload := createTestWorkloadWithService(true)
		err := workloadController.Processor.handleWorkload(workload)
		assert.NoError(t, err)
	}
	workloadController.Processor.hashName.Reset()
}

func createTestWorkloadWithService(withService bool) *workloadapi.Workload {
	workload := workloadapi.Workload{
		Namespace:         "ns",
		Name:              "name",
		Addresses:         [][]byte{netip.AddrFrom4([4]byte{1, 2, 3, 4}).AsSlice()},
		Network:           "testnetwork",
		CanonicalName:     "foo",
		CanonicalRevision: "latest",
		WorkloadType:      workloadapi.WorkloadType_POD,
		WorkloadName:      "name",
		Status:            workloadapi.WorkloadStatus_HEALTHY,
		ClusterId:         "cluster0",
		Services:          map[string]*workloadapi.PortList{},
	}

	if withService == true {
		workload.Services = map[string]*workloadapi.PortList{
			"default/testsvc.default.svc.cluster.local": {
				Ports: []*workloadapi.Port{
					{
						ServicePort: 80,
						TargetPort:  8080,
					},
					{
						ServicePort: 81,
						TargetPort:  8180,
					},
					{
						ServicePort: 82,
						TargetPort:  82,
					},
				},
			},
		}
	}
	workload.Uid = "cluster0/" + rand.String(6)
	return &workload
}

func createFakeWorkload(ip string, networkMode workloadapi.NetworkMode) *workloadapi.Workload {
	workload := workloadapi.Workload{
		Namespace:         "ns",
		Name:              "name",
		Addresses:         [][]byte{netip.MustParseAddr(ip).AsSlice()},
		Network:           "testnetwork",
		CanonicalName:     "foo",
		CanonicalRevision: "latest",
		WorkloadType:      workloadapi.WorkloadType_POD,
		WorkloadName:      "name",
		Status:            workloadapi.WorkloadStatus_HEALTHY,
		ClusterId:         "cluster0",
		NetworkMode:       networkMode,
		Services: map[string]*workloadapi.PortList{
			"default/testsvc.default.svc.cluster.local": {
				Ports: []*workloadapi.Port{
					{
						ServicePort: 80,
						TargetPort:  8080,
					},
					{
						ServicePort: 81,
						TargetPort:  8180,
					},
					{
						ServicePort: 82,
						TargetPort:  82,
					},
				},
			},
		},
	}
	workload.Uid = "cluster0/" + rand.String(6)
	return &workload
}

func createFakeService(name, ip, waypoint string) *workloadapi.Service {
	return &workloadapi.Service{
		Name:      name,
		Namespace: "default",
		Hostname:  name + ".default.svc.cluster.local",
		Addresses: []*workloadapi.NetworkAddress{
			{
				Address: netip.MustParseAddr(ip).AsSlice(),
			},
		},
		Ports: []*workloadapi.Port{
			{
				ServicePort: 80,
				TargetPort:  8080,
			},
			{
				ServicePort: 81,
				TargetPort:  8180,
			},
			{
				ServicePort: 82,
				TargetPort:  82,
			},
		},
		Waypoint: &workloadapi.GatewayAddress{
			Destination: &workloadapi.GatewayAddress_Address{
				Address: &workloadapi.NetworkAddress{
					Address: netip.MustParseAddr(waypoint).AsSlice(),
				},
			},
			HboneMtlsPort: 15008,
		},
	}
}

func createWorkload(name, ip string, networkload workloadapi.NetworkMode, services ...string) *workloadapi.Workload {
	workload := workloadapi.Workload{
		Uid:               "cluster0//Pod/default/" + name,
		Namespace:         "default",
		Name:              name,
		Addresses:         [][]byte{netip.MustParseAddr(ip).AsSlice()},
		Network:           "testnetwork",
		CanonicalName:     "foo",
		CanonicalRevision: "latest",
		WorkloadType:      workloadapi.WorkloadType_POD,
		WorkloadName:      "name",
		Status:            workloadapi.WorkloadStatus_HEALTHY,
		ClusterId:         "cluster0",
		NetworkMode:       networkload,
	}
	workload.Services = make(map[string]*workloadapi.PortList, len(services))
	for _, svc := range services {
		workload.Services["default/"+svc+".default.svc.cluster.local"] = &workloadapi.PortList{
			Ports: []*workloadapi.Port{
				{
					ServicePort: 80,
					TargetPort:  8080,
				},
				{
					ServicePort: 81,
					TargetPort:  8180,
				},
				{
					ServicePort: 82,
					TargetPort:  82,
				},
			},
		}
	}
	return &workload
}

func TestRestart(t *testing.T) {
	workloadMap := bpfcache.NewFakeWorkloadMap(t)
	defer bpfcache.CleanupFakeWorkloadMap(workloadMap)

	p := newProcessor(workloadMap)

	res := &service_discovery_v3.DeltaDiscoveryResponse{}

	// 1. First simulate normal start
	// 1.1 add related service
	svc1 := createFakeService("svc1", "10.240.10.1", "10.240.10.200")
	svc2 := createFakeService("svc2", "10.240.10.2", "10.240.10.200")
	svc3 := createFakeService("svc3", "10.240.10.3", "10.240.10.200")
	for _, svc := range []*workloadapi.Service{svc1, svc2, svc3} {
		addr := serviceToAddress(svc)
		res.Resources = append(res.Resources, &service_discovery_v3.Resource{
			Resource: protoconv.MessageToAny(addr),
		})
	}

	// 1.2 add workload
	wl1 := createWorkload("wl1", "10.244.0.1", workloadapi.NetworkMode_STANDARD, "svc1", "svc2")
	wl2 := createWorkload("wl2", "10.244.0.2", workloadapi.NetworkMode_STANDARD, "svc2", "svc3")
	wl3 := createWorkload("wl3", "10.244.0.3", workloadapi.NetworkMode_STANDARD, "svc3")
	for _, wl := range []*workloadapi.Workload{wl1, wl2, wl3} {
		addr := workloadToAddress(wl)
		res.Resources = append(res.Resources, &service_discovery_v3.Resource{
			Resource: protoconv.MessageToAny(addr),
		})
	}

	err := p.handleAddressTypeResponse(res)
	assert.NoError(t, err)

	// check front end map
	for _, wl := range []*workloadapi.Workload{wl1, wl2, wl3} {
		checkFrontEndMap(t, wl.Addresses[0], p)
	}
	for _, svc := range []*workloadapi.Service{svc1, svc2, svc3} {
		checkFrontEndMap(t, svc.Addresses[0].Address, p)
	}
	// check service map
	t.Log("1. check service map")
	checkServiceMap(t, p, p.hashName.Hash(svc1.ResourceName()), svc1, 1)
	checkServiceMap(t, p, p.hashName.Hash(svc2.ResourceName()), svc2, 2)
	checkServiceMap(t, p, p.hashName.Hash(svc3.ResourceName()), svc3, 2)
	// check endpoint map
	t.Log("1. check endpoint map")
	checkEndpointMap(t, p, svc1, []uint32{p.hashName.Hash(wl1.ResourceName())})
	checkEndpointMap(t, p, svc2, []uint32{p.hashName.Hash(wl1.ResourceName()), p.hashName.Hash(wl2.ResourceName())})
	checkEndpointMap(t, p, svc3, []uint32{p.hashName.Hash(wl2.ResourceName()), p.hashName.Hash(wl3.ResourceName())})
	// check backend map
	for _, wl := range []*workloadapi.Workload{wl1, wl2, wl3} {
		checkBackendMap(t, p, p.hashName.Hash(wl.ResourceName()), wl)
	}

	// 2. Second simulate restart
	// Set a restart label and simulate missing data in the cache
	bpf.SetStartType(bpf.Restart)
	// reconstruct a new processor
	p = newProcessor(workloadMap)
	p.bpf.RestoreEndpointKeys()
	// 2.1 simulate workload add/delete during restart
	// simulate workload update during restart

	// wl1 now only belong to svc1
	delete(wl1.Services, "default/svc2.default.svc.cluster.local")
	// wl2 now belong to svc1, svc2, svc3
	wl2.Services["default/svc1.default.svc.cluster.local"] = &workloadapi.PortList{
		Ports: []*workloadapi.Port{
			{
				ServicePort: 80,
				TargetPort:  8080,
			},
			{
				ServicePort: 81,
				TargetPort:  8180,
			},
			{
				ServicePort: 82,
				TargetPort:  82,
			},
		},
	}

	wl4 := createWorkload("wl4", "10.244.0.4", workloadapi.NetworkMode_STANDARD, "svc4")
	svc4 := createFakeService("svc4", "10.240.10.4", "10.240.10.200")

	res = &service_discovery_v3.DeltaDiscoveryResponse{}
	// wl3 deleted during restart
	for _, wl := range []*workloadapi.Workload{wl1, wl2, wl4} {
		addr := workloadToAddress(wl)
		res.Resources = append(res.Resources, &service_discovery_v3.Resource{
			Resource: protoconv.MessageToAny(addr),
		})
	}

	for _, svc := range []*workloadapi.Service{svc1, svc2, svc3, svc4} {
		addr := serviceToAddress(svc)
		res.Resources = append(res.Resources, &service_discovery_v3.Resource{
			Resource: protoconv.MessageToAny(addr),
		})
	}

	err = p.handleAddressTypeResponse(res)
	assert.NoError(t, err)

	// check front end map
	t.Log("2. check front end map")
	for _, wl := range []*workloadapi.Workload{wl1, wl2, wl4} {
		checkFrontEndMap(t, wl.Addresses[0], p)
	}
	for _, svc := range []*workloadapi.Service{svc1, svc2, svc3, svc4} {
		checkFrontEndMap(t, svc.Addresses[0].Address, p)
	}
	// TODO(hzxuzhonghu) check front end map elements number

	// check service map
	checkServiceMap(t, p, p.hashName.Hash(svc1.ResourceName()), svc1, 2) // svc1 has 2 wl1, wl2
	checkServiceMap(t, p, p.hashName.Hash(svc2.ResourceName()), svc2, 1) // svc2 has 1  wl2
	checkServiceMap(t, p, p.hashName.Hash(svc3.ResourceName()), svc3, 1) // svc3 has 1  wl2
	checkServiceMap(t, p, p.hashName.Hash(svc4.ResourceName()), svc4, 1) // svc4 has 1  wl4
	// check endpoint map
	checkEndpointMap(t, p, svc1, []uint32{p.hashName.Hash(wl1.ResourceName()), p.hashName.Hash(wl2.ResourceName())})
	checkEndpointMap(t, p, svc2, []uint32{p.hashName.Hash(wl2.ResourceName())})
	checkEndpointMap(t, p, svc3, []uint32{p.hashName.Hash(wl2.ResourceName())})
	checkEndpointMap(t, p, svc4, []uint32{p.hashName.Hash(wl4.ResourceName())})
	// check backend map
	for _, wl := range []*workloadapi.Workload{wl1, wl2, wl4} {
		checkBackendMap(t, p, p.hashName.Hash(wl.ResourceName()), wl)
	}

	hashNameClean(p)
}

// The hashname will be saved as a file by default.
// If it is not cleaned, it will affect other use cases.
func hashNameClean(p *Processor) {
	for str := range p.hashName.strToNum {
		if err := p.removeWorkloadFromBpfMap(str); err != nil {
			log.Errorf("RemoveWorkloadResource failed: %v", err)
		}

		if err := p.removeServiceResourceFromBpfMap(nil, str); err != nil {
			log.Errorf("RemoveServiceResource failed: %v", err)
		}
		p.hashName.Delete(str)
	}
	p.hashName.Reset()
}

func workloadToAddress(wl *workloadapi.Workload) *workloadapi.Address {
	return &workloadapi.Address{
		Type: &workloadapi.Address_Workload{
			Workload: wl,
		},
	}
}

func serviceToAddress(service *workloadapi.Service) *workloadapi.Address {
	return &workloadapi.Address{
		Type: &workloadapi.Address_Service{
			Service: service,
		},
	}
}
