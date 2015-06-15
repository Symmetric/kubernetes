/*
Copyright 2015 The Kubernetes Authors All rights reserved.

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

package proxy

import (
	"sync"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/types"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/slice"
	"github.com/golang/glog"
)

type DummyLoadBalancer interface {
	NewService(svcPort ServicePortName, affinityType api.ServiceAffinity) error
	OnUpdate(allEndpoints []api.Endpoints)
	GetService(service ServicePortName) (*dummyBalancerState, bool)
}

type dummyAffinityPolicy struct {
	affinityType api.ServiceAffinity
	affinityMap  map[string]*affinityState // map client IP -> affinity info
}

func newDummyAffinityPolicy(affinityType api.ServiceAffinity) *dummyAffinityPolicy {
	return &dummyAffinityPolicy{
		affinityType: affinityType,
		affinityMap:  make(map[string]*affinityState),
	}
}

type dummyBalancerState struct {
	endpoints []string
	affinity  dummyAffinityPolicy
}

// DummyLoadBalancerIptables is used by proxierIptables as a receiver of endpoint changes.
// NOTE: this does *not* implement the LoadBalancer interface as it is not a real LoadBalancer,
// this merely forwards OnUpdate events to the proxy via a callback.
// TODO: This is a bit of a hack. See Comments on OnUpdate below.
type DummyLoadBalancerIptables struct {
	lock     sync.Mutex
	services map[ServicePortName]*dummyBalancerState
}

func (lb *DummyLoadBalancerIptables) GetService(service ServicePortName) (*dummyBalancerState, bool) {
	lb.lock.Lock()
	defer lb.lock.Unlock()
	info, exists := lb.services[service]
	return info, exists
}

// Returns a new DummyLoadBalancerIptables
func NewDummyLoadBalancerIptables() *DummyLoadBalancerIptables {
	return &DummyLoadBalancerIptables{
		services: make(map[ServicePortName]*dummyBalancerState),
	}
}

// This assumes that lb.lock is already held.
func (lb *DummyLoadBalancerIptables) newServiceInternal(svcPort ServicePortName, affinityType api.ServiceAffinity) *dummyBalancerState {
	if _, exists := lb.services[svcPort]; !exists {
		lb.services[svcPort] = &dummyBalancerState{affinity: *newDummyAffinityPolicy(affinityType)}
		glog.V(4).Infof("DummyLoadBalancerIptables service %q did not exist, created", svcPort)
	}
	return lb.services[svcPort]
}

func (lb *DummyLoadBalancerIptables) NewService(svcPort ServicePortName, affinityType api.ServiceAffinity) error {
	lb.lock.Lock()
	defer lb.lock.Unlock()
	lb.newServiceInternal(svcPort, affinityType)
	return nil
}

// Loop through the valid endpoints and then the endpoints associated with the Load Balancer.
// Then remove any session affinity records that are not in both lists.
// This assumes the lb.lock is held.
func (lb *DummyLoadBalancerIptables) updateAffinityMap(svcPort ServicePortName, newEndpoints []string) {
	allEndpoints := map[string]int{}
	for _, newEndpoint := range newEndpoints {
		allEndpoints[newEndpoint] = 1
	}
	state, exists := lb.services[svcPort]
	if !exists {
		return
	}
	for _, existingEndpoint := range state.endpoints {
		allEndpoints[existingEndpoint] = allEndpoints[existingEndpoint] + 1
	}
	for mKey, mVal := range allEndpoints {
		if mVal == 1 {
			//This is from roundrobin.go and doesn't make sense? We aren't deleting the
			//endpoint. We're removing session affinity records.
			//glog.V(2).Infof("Delete endpoint %s for service %q", mKey, svcPort)
			lb.removeSessionAffinityByEndpoint(state, svcPort, mKey)
		}
	}
}

// Remove any session affinity records associated to a particular endpoint (for example when a pod goes down).
func (lb *DummyLoadBalancerIptables) removeSessionAffinityByEndpoint(state *dummyBalancerState, svcPort ServicePortName, endpoint string) {
	for _, affinity := range state.affinity.affinityMap {
		if affinity.endpoint == endpoint {
			glog.V(4).Infof("Removing client: %s from affinityMap for service %q", affinity.endpoint, svcPort)
			delete(state.affinity.affinityMap, affinity.clientIP)
		}
	}
}

// OnUpdate takes in a slice of updated endpoints.
// because all actual load-balancing is done in ProxierIptables we just keep
// track of the endpoints and map them to services
// We can't just handle OnUpdate in ProxierIptables because Proxier already has a
// clashing OnUpdate function.
// TODO: we may want to make this hack obsolete by redesigning the relevant
// OnUpdate apis.
func (lb *DummyLoadBalancerIptables) OnUpdate(allEndpoints []api.Endpoints) {
	registeredEndpoints := make(map[ServicePortName]bool)
	lb.lock.Lock()
	defer lb.lock.Unlock()

	// Update endpoints for services.
	for i := range allEndpoints {
		svcEndpoints := &allEndpoints[i]

		// We need to build a map of portname -> all ip:ports for that
		// portname.  Explode Endpoints.Subsets[*] into this structure.
		portsToEndpoints := map[string][]hostPortPair{}
		for i := range svcEndpoints.Subsets {
			ss := &svcEndpoints.Subsets[i]
			for i := range ss.Ports {
				port := &ss.Ports[i]
				for i := range ss.Addresses {
					addr := &ss.Addresses[i]
					portsToEndpoints[port.Name] = append(portsToEndpoints[port.Name], hostPortPair{addr.IP, port.Port})
					// Ignore the protocol field - we'll get that from the Service objects.
				}
			}
		}

		for portname := range portsToEndpoints {
			svcPort := ServicePortName{types.NamespacedName{svcEndpoints.Namespace, svcEndpoints.Name}, portname}
			state, exists := lb.services[svcPort]
			curEndpoints := []string{}
			if state != nil {
				curEndpoints = state.endpoints
			}
			newEndpoints := flattenValidEndpoints(portsToEndpoints[portname])

			if !exists || state == nil || len(curEndpoints) != len(newEndpoints) || !slicesEquiv(slice.CopyStrings(curEndpoints), newEndpoints) {
				glog.V(1).Infof("DummyLoadBalancerIptables: Setting endpoints for %s to %+v", svcPort, newEndpoints)
				lb.updateAffinityMap(svcPort, newEndpoints)
				// OnUpdate can be called without NewService being called externally.
				// To be safe we will call it here.  A new service will only be created
				// if one does not already exist.
				state = lb.newServiceInternal(svcPort, api.ServiceAffinity(""))
				state.endpoints = newEndpoints
			}
			registeredEndpoints[svcPort] = true
		}
	}
	// Remove endpoints missing from the update.
	for k := range lb.services {
		if _, exists := registeredEndpoints[k]; !exists {
			glog.V(2).Infof("DummyLoadBalancerIptables: Removing endpoints for %s", k)
			delete(lb.services, k)
		}
	}
}
