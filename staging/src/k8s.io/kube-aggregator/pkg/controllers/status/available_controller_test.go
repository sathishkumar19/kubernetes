/*
Copyright 2017 The Kubernetes Authors.

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

package apiserver

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kube-aggregator/pkg/apis/apiregistration"
	"k8s.io/kube-aggregator/pkg/client/clientset_generated/internalclientset/fake"
	listers "k8s.io/kube-aggregator/pkg/client/listers/apiregistration/internalversion"
	"k8s.io/kubernetes/pkg/api"
	internallisters "k8s.io/kubernetes/pkg/client/listers/core/internalversion"
)

func newEndpoints(namespace, name string) *api.Endpoints {
	return &api.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

func newEndpointsWithAddress(namespace, name string) *api.Endpoints {
	return &api.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Subsets: []api.EndpointSubset{
			{
				Addresses: []api.EndpointAddress{
					{
						IP: "val",
					},
				},
			},
		},
	}
}

func newService(namespace, name string) *api.Service {
	return &api.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: api.ServiceSpec{
			Type: api.ServiceTypeClusterIP,
		},
	}
}

func newLocalAPIService(name string) *apiregistration.APIService {
	return &apiregistration.APIService{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func newRemoteAPIService(name string) *apiregistration.APIService {
	return &apiregistration.APIService{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiregistration.APIServiceSpec{
			Service: &apiregistration.ServiceReference{
				Namespace: "foo",
				Name:      "bar",
			},
		},
	}
}

func TestSync(t *testing.T) {
	tests := []struct {
		name string

		apiServiceName       string
		apiServices          []*apiregistration.APIService
		services             []*api.Service
		endpoints            []*api.Endpoints
		expectedAvailability apiregistration.APIServiceCondition
	}{
		{
			name:           "local",
			apiServiceName: "local.group",
			apiServices:    []*apiregistration.APIService{newLocalAPIService("local.group")},
			expectedAvailability: apiregistration.APIServiceCondition{
				Type:    apiregistration.Available,
				Status:  apiregistration.ConditionTrue,
				Reason:  "Local",
				Message: "Local APIServices are always available",
			},
		},
		{
			name:           "no service",
			apiServiceName: "remote.group",
			apiServices:    []*apiregistration.APIService{newRemoteAPIService("remote.group")},
			services:       []*api.Service{newService("foo", "not-bar")},
			expectedAvailability: apiregistration.APIServiceCondition{
				Type:    apiregistration.Available,
				Status:  apiregistration.ConditionFalse,
				Reason:  "ServiceNotFound",
				Message: `service/bar in "foo" is not present`,
			},
		},
		{
			name:           "no endpoints",
			apiServiceName: "remote.group",
			apiServices:    []*apiregistration.APIService{newRemoteAPIService("remote.group")},
			services:       []*api.Service{newService("foo", "bar")},
			expectedAvailability: apiregistration.APIServiceCondition{
				Type:    apiregistration.Available,
				Status:  apiregistration.ConditionFalse,
				Reason:  "EndpointsNotFound",
				Message: `cannot find endpoints for service/bar in "foo"`,
			},
		},
		{
			name:           "missing endpoints",
			apiServiceName: "remote.group",
			apiServices:    []*apiregistration.APIService{newRemoteAPIService("remote.group")},
			services:       []*api.Service{newService("foo", "bar")},
			endpoints:      []*api.Endpoints{newEndpoints("foo", "bar")},
			expectedAvailability: apiregistration.APIServiceCondition{
				Type:    apiregistration.Available,
				Status:  apiregistration.ConditionFalse,
				Reason:  "MissingEndpoints",
				Message: `endpoints for service/bar in "foo" have no addresses`,
			},
		},
		{
			name:           "remote",
			apiServiceName: "remote.group",
			apiServices:    []*apiregistration.APIService{newRemoteAPIService("remote.group")},
			services:       []*api.Service{newService("foo", "bar")},
			endpoints:      []*api.Endpoints{newEndpointsWithAddress("foo", "bar")},
			expectedAvailability: apiregistration.APIServiceCondition{
				// this is different in the 1.7 pull where the check is skipped.
				Type:    apiregistration.Available,
				Status:  apiregistration.ConditionFalse,
				Reason:  "FailedDiscoveryCheck",
				Message: `no response from https:///apis: context deadline exceeded`,
			},
		},
	}

	for _, tc := range tests {
		fakeClient := fake.NewSimpleClientset()
		apiServiceIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		serviceIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		endpointsIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		for _, obj := range tc.apiServices {
			apiServiceIndexer.Add(obj)
		}
		for _, obj := range tc.services {
			serviceIndexer.Add(obj)
		}
		for _, obj := range tc.endpoints {
			endpointsIndexer.Add(obj)
		}

		c := AvailableConditionController{
			apiServiceClient: fakeClient.Apiregistration(),
			apiServiceLister: listers.NewAPIServiceLister(apiServiceIndexer),
			serviceLister:    internallisters.NewServiceLister(serviceIndexer),
			endpointsLister:  internallisters.NewEndpointsLister(endpointsIndexer),
		}
		c.sync(tc.apiServiceName)

		// ought to have one action writing status
		if e, a := 1, len(fakeClient.Actions()); e != a {
			t.Errorf("%v expected %v, got %v", tc.name, e, fakeClient.Actions())
			continue
		}

		action, ok := fakeClient.Actions()[0].(clienttesting.UpdateAction)
		if !ok {
			t.Errorf("%v got %v", tc.name, ok)
			continue
		}

		if e, a := 1, len(action.GetObject().(*apiregistration.APIService).Status.Conditions); e != a {
			t.Errorf("%v expected %v, got %v", tc.name, e, action.GetObject())
			continue
		}
		condition := action.GetObject().(*apiregistration.APIService).Status.Conditions[0]
		if e, a := tc.expectedAvailability.Type, condition.Type; e != a {
			t.Errorf("%v expected %v, got %#v", tc.name, e, condition)
		}
		if e, a := tc.expectedAvailability.Status, condition.Status; e != a {
			t.Errorf("%v expected %v, got %#v", tc.name, e, condition)
		}
		if e, a := tc.expectedAvailability.Reason, condition.Reason; e != a {
			t.Errorf("%v expected %v, got %#v", tc.name, e, condition)
		}
		if e, a := tc.expectedAvailability.Message, condition.Message; e != a {
			t.Errorf("%v expected %v, got %#v", tc.name, e, condition)
		}
	}
}
