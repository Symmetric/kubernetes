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
	"testing"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/types"
)

func TestDummyLBTracksMultipleEndpoints(t *testing.T) {
	loadBalancer := NewDummyLoadBalancerIptables()
	testDummyLBLoadBalanceFailsWithNoEndpoints(t, loadBalancer)

	service := ServicePortName{types.NamespacedName{"testnamespace", "foo"}, "p"}
	endpoints := make([]api.Endpoints, 1)
	endpoints[0] = api.Endpoints{
		ObjectMeta: api.ObjectMeta{Name: service.Name, Namespace: service.Namespace},
		Subsets: []api.EndpointSubset{{
			Addresses: []api.EndpointAddress{{IP: "endpoint"}},
			Ports:     []api.EndpointPort{{Name: "p", Port: 1}, {Name: "p", Port: 2}, {Name: "p", Port: 3}},
		}},
	}
	loadBalancer.OnUpdate(endpoints)
	endpointInfo, exists := loadBalancer.GetService(service)
	if exists != true {
		t.Errorf("Didn't get endpoints for existing service")
	}
	if len(endpointInfo.endpoints) != 3 {
		t.Errorf("Wrong number of endpoints")
	}
}

func testDummyLBLoadBalanceFailsWithNoEndpoints(t *testing.T, loadBalancer *DummyLoadBalancerIptables) {
	var endpoints []api.Endpoints
	loadBalancer.OnUpdate(endpoints)
	service := ServicePortName{types.NamespacedName{"testnamespace", "foo"}, "does-not-exist"}
	if _, exists := loadBalancer.GetService(service); exists {
		t.Errorf("Didn't fail with non-existent service")
	}
}
func TestDummyLBLoadBalanceFailsWithNoEndpoints(t *testing.T) {
	loadBalancer := NewDummyLoadBalancerIptables()
	testDummyLBLoadBalanceFailsWithNoEndpoints(t, loadBalancer)

}

func TestDummyLBWorksWithMultipleEndpointsAndUpdates(t *testing.T) {
	loadBalancer := NewDummyLoadBalancerIptables()
	testDummyLBLoadBalanceFailsWithNoEndpoints(t, loadBalancer)

	service := ServicePortName{types.NamespacedName{"testnamespace", "foo"}, "does-not-exist"}
	serviceP := ServicePortName{types.NamespacedName{"testnamespace", "foo"}, "p"}
	serviceQ := ServicePortName{types.NamespacedName{"testnamespace", "foo"}, "q"}

	endpoints := make([]api.Endpoints, 1)
	endpoints[0] = api.Endpoints{
		ObjectMeta: api.ObjectMeta{Name: serviceP.Name, Namespace: serviceP.Namespace},
		Subsets: []api.EndpointSubset{
			{
				Addresses: []api.EndpointAddress{{IP: "endpoint1"}},
				Ports:     []api.EndpointPort{{Name: "p", Port: 1}, {Name: "q", Port: 10}},
			},
			{
				Addresses: []api.EndpointAddress{{IP: "endpoint2"}},
				Ports:     []api.EndpointPort{{Name: "p", Port: 2}, {Name: "q", Port: 20}},
			},
			{
				Addresses: []api.EndpointAddress{{IP: "endpoint3"}},
				Ports:     []api.EndpointPort{{Name: "p", Port: 3}, {Name: "q", Port: 30}},
			},
		},
	}
	loadBalancer.OnUpdate(endpoints)

	if _, exists := loadBalancer.GetService(service); exists {
		t.Errorf("Didn't fail with non-existent service")
	}

	endpointInfo, exists := loadBalancer.GetService(serviceP)
	if !exists {
		t.Errorf("Failed to get existing service")
	}
	if exists && len(endpointInfo.endpoints) != 3 {
		t.Errorf("Incorrect number of endpoints for service")
	}

	endpointInfo, exists = loadBalancer.GetService(serviceQ)
	if !exists {
		t.Error("Failed to get existing service")
	}
	if exists && len(endpointInfo.endpoints) != 3 {
		t.Errorf("Incorrect number of endpoints for service")
	}

	//set fewer endpoints
	endpoints[0] = api.Endpoints{
		ObjectMeta: api.ObjectMeta{Name: serviceP.Name, Namespace: serviceP.Namespace},
		Subsets: []api.EndpointSubset{
			{
				Addresses: []api.EndpointAddress{{IP: "endpoint4"}},
				Ports:     []api.EndpointPort{{Name: "p", Port: 4}, {Name: "q", Port: 40}},
			},
			{
				Addresses: []api.EndpointAddress{{IP: "endpoint5"}},
				Ports:     []api.EndpointPort{{Name: "p", Port: 5}, {Name: "q", Port: 50}},
			},
		},
	}
	loadBalancer.OnUpdate(endpoints)

	if _, exists := loadBalancer.GetService(service); exists {
		t.Errorf("Didn't fail with non-existent service")
	}

	endpointInfo, exists = loadBalancer.GetService(serviceP)
	if !exists {
		t.Errorf("Failed to get existing service")
	}
	if exists && len(endpointInfo.endpoints) != 2 {
		t.Errorf("Incorrect number of endpoints for service")
	}

	endpointInfo, exists = loadBalancer.GetService(serviceQ)
	if !exists {
		t.Errorf("Failed to get existing service")
	}
	if exists && len(endpointInfo.endpoints) != 2 {
		t.Errorf("Incorrect number of endpoints for service")
	}
}

func TestDummyLBServiceRemoval(t *testing.T) {
	loadBalancer := NewDummyLoadBalancerIptables()

	badService := ServicePortName{types.NamespacedName{"testnamespace", "bad"}, "does-not-exist"}
	fooServiceP := ServicePortName{types.NamespacedName{"testnamespace", "foo"}, "p"}
	barServiceP := ServicePortName{types.NamespacedName{"testnamespace", "bar"}, "p"}

	endpoints := make([]api.Endpoints, 2)
	endpoints[0] = api.Endpoints{
		ObjectMeta: api.ObjectMeta{Name: fooServiceP.Name, Namespace: fooServiceP.Namespace},
		Subsets: []api.EndpointSubset{
			{
				Addresses: []api.EndpointAddress{{IP: "endpoint1"}, {IP: "endpoint2"}, {IP: "endpoint3"}},
				Ports:     []api.EndpointPort{{Name: "p", Port: 123}},
			},
		},
	}
	endpoints[1] = api.Endpoints{
		ObjectMeta: api.ObjectMeta{Name: barServiceP.Name, Namespace: barServiceP.Namespace},
		Subsets: []api.EndpointSubset{
			{
				Addresses: []api.EndpointAddress{{IP: "endpoint4"}, {IP: "endpoint5"}, {IP: "endpoint6"}},
				Ports:     []api.EndpointPort{{Name: "p", Port: 456}},
			},
		},
	}
	loadBalancer.OnUpdate(endpoints)

	if _, exists := loadBalancer.GetService(badService); exists {
		t.Errorf("Didn't fail with non-existent service")
	}

	endpointInfo, exists := loadBalancer.GetService(fooServiceP)
	if !exists {
		t.Errorf("Failed to get existing service")
	}
	if exists && len(endpointInfo.endpoints) != 3 {
		t.Errorf("Incorrect number of endpoints for service")
	}

	endpointInfo, exists = loadBalancer.GetService(barServiceP)
	if !exists {
		t.Errorf("Failed to get existing service")
	}
	if exists && len(endpointInfo.endpoints) != 3 {
		t.Errorf("Incorrect number of endpoints for service")
	}

	//remove fooServiceP
	loadBalancer.OnUpdate(endpoints[1:])

	if _, exists := loadBalancer.GetService(badService); exists {
		t.Errorf("Didn't fail with non-existent service")
	}

	if _, exists := loadBalancer.GetService(fooServiceP); exists {
		t.Errorf("Didn't fail with non-existent service")
	}

	endpointInfo, exists = loadBalancer.GetService(barServiceP)
	if !exists {
		t.Errorf("Failed to get existing service")
	}
	if exists && len(endpointInfo.endpoints) != 3 {
		t.Errorf("Incorrect number of endpoints for service")
	}
}
