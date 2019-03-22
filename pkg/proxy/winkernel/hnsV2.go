// +build windows

/*
Copyright 2018 The Kubernetes Authors.

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

package winkernel

import (
	"encoding/json"
	"fmt"

	"github.com/Microsoft/hcsshim/hcn"
	"k8s.io/klog"

	"strings"
)

type hnsV2 struct{}

func (hns hnsV2) getNetworkByName(name string) (*hnsNetworkInfo, error) {
	hnsnetwork, err := hcn.GetNetworkByName(name)
	if err != nil {
		klog.Errorf("%v", err)
		return nil, err
	}

	var remoteSubnets []*remoteSubnetInfo
	for _, policy := range hnsnetwork.Policies {
		if policy.Type == hcn.RemoteSubnetRoute {
			policySettings := hcn.RemoteSubnetRoutePolicySetting{}
			err = json.Unmarshal(policy.Settings, &policySettings)
			if err != nil {
				return nil, fmt.Errorf("Failed to unmarshal Remote Subnet policy settings")
			}
			rs := &remoteSubnetInfo{
				destinationPrefix: policySettings.DestinationPrefix,
				isolationId:       policySettings.IsolationId,
				providerAddress:   policySettings.ProviderAddress,
				drMacAddress:      policySettings.DistributedRouterMacAddress,
			}
			remoteSubnets = append(remoteSubnets, rs)
		}
	}

	return &hnsNetworkInfo{
		id:            hnsnetwork.Id,
		name:          hnsnetwork.Name,
		networkType:   string(hnsnetwork.Type),
		remoteSubnets: remoteSubnets,
	}, nil
}
func (hns hnsV2) getEndpointByID(id string) (*endpointsInfo, error) {
	hnsendpoint, err := hcn.GetEndpointByID(id)
	if err != nil {
		return nil, err
	}
	pol := transformToPoliciesinfo(hnsendpoint.Policies)
	return &endpointsInfo{ //TODO: fill out PA
		ip:         hnsendpoint.IpConfigurations[0].IpAddress,
		isLocal:    uint32(hnsendpoint.Flags&hcn.EndpointFlagsRemoteEndpoint) == 0, //TODO: Change isLocal to isRemote
		macAddress: hnsendpoint.MacAddress,
		hnsID:      hnsendpoint.Id,
		hns:        hns,
		policies:   pol,
	}, nil
}
func (hns hnsV2) getEndpointByIpAddress(ip string, networkName string) (*endpointsInfo, error) {
	hnsnetwork, err := hcn.GetNetworkByName(networkName)
	if err != nil {
		klog.Errorf("%v", err)
		return nil, err
	}

	endpoints, err := hcn.ListEndpoints()
	for _, endpoint := range endpoints {
		equal := false
		if endpoint.IpConfigurations != nil && len(endpoint.IpConfigurations) > 0 {
			equal = endpoint.IpConfigurations[0].IpAddress == ip
		}
		if equal && strings.EqualFold(endpoint.HostComputeNetwork, hnsnetwork.Id) {
			pol := transformToPoliciesinfo(endpoint.Policies)
			return &endpointsInfo{
				ip:         endpoint.IpConfigurations[0].IpAddress,
				isLocal:    uint32(endpoint.Flags&hcn.EndpointFlagsRemoteEndpoint) == 0, //TODO: Change isLocal to isRemote
				macAddress: endpoint.MacAddress,
				hnsID:      endpoint.Id,
				hns:        hns,
				policies:   pol,
			}, nil
		}
	}

	return nil, fmt.Errorf("Endpoint %v not found on network %s", ip, networkName)
}
func transformToPoliciesinfo(hcnendpointpolicies []hcn.EndpointPolicy) []*policiesinfo {
	var endpointPolicies []*policiesinfo
	for _, po := range hcnendpointpolicies {
		var policy policiesinfo
		policy.Type = string(po.Type)
		policy.Settings = po.Settings
		endpointPolicies = append(endpointPolicies, &policy)
		klog.V(3).Infof("endpoint policy:%s", policy)
	}
	return endpointPolicies
}
func (hns hnsV2) createEndpoint(ep *endpointsInfo, networkName string) (*endpointsInfo, error) {
	hnsNetwork, err := hcn.GetNetworkByName(networkName)
	if err != nil {
		return nil, fmt.Errorf("Could not find network %s: %v", networkName, err)
	}
	var flags hcn.EndpointFlags
	if !ep.isLocal {
		flags |= hcn.EndpointFlagsRemoteEndpoint
	}
	ipConfig := &hcn.IpConfig{
		IpAddress: ep.ip,
	}
	hnsEndpoint := &hcn.HostComputeEndpoint{
		IpConfigurations: []hcn.IpConfig{*ipConfig},
		MacAddress:       ep.macAddress,
		Flags:            flags,
		SchemaVersion: hcn.SchemaVersion{
			Major: 2,
			Minor: 0,
		},
	}

	var createdEndpoint *hcn.HostComputeEndpoint
	if !ep.isLocal {
		if len(ep.providerAddress) != 0 {
			policySettings := hcn.ProviderAddressEndpointPolicySetting{
				ProviderAddress: ep.providerAddress,
			}
			policySettingsJson, err := json.Marshal(policySettings)
			if err != nil {
				return nil, fmt.Errorf("PA Policy creation failed: %v", err)
			}
			paPolicy := hcn.EndpointPolicy{
				Type:     hcn.NetworkProviderAddress,
				Settings: policySettingsJson,
			}
			hnsEndpoint.Policies = append(hnsEndpoint.Policies, paPolicy)
		}
		createdEndpoint, err = hnsNetwork.CreateRemoteEndpoint(hnsEndpoint)
		if err != nil {
			return nil, fmt.Errorf("Remote endpoint creation failed: %v", err)
		}
	} else {
		createdEndpoint, err = hnsNetwork.CreateEndpoint(hnsEndpoint)
		if err != nil {
			return nil, fmt.Errorf("Local endpoint creation failed: %v", err)
		}
	}
	pol := transformToPoliciesinfo(createdEndpoint.Policies)
	return &endpointsInfo{
		ip:              createdEndpoint.IpConfigurations[0].IpAddress,
		isLocal:         uint32(createdEndpoint.Flags&hcn.EndpointFlagsRemoteEndpoint) == 0,
		macAddress:      createdEndpoint.MacAddress,
		hnsID:           createdEndpoint.Id,
		providerAddress: ep.providerAddress, //TODO get from createdEndpoint
		hns:             hns,
		policies:        pol,
	}, nil
}
func (hns hnsV2) updateEndpointPolicy(hnsID string, policy json.RawMessage) error {
	requestMessage := &hcn.ModifyEndpointSettingRequest{
		ResourceType: "Policy",
		RequestType:  "Add",
		Settings:     policy,
	}

	klog.V(3).Infof("Local endpoint policy added to %s", hnsID)
	LogJson(policy, "Local endpoint policy:", 1)

	return hcn.ModifyEndpointSettings(hnsID, requestMessage)
}
func (hns hnsV2) deleteEndpoint(hnsID string) error {
	hnsendpoint, err := hcn.GetEndpointByID(hnsID)
	if err != nil {
		return err
	}
	err = hnsendpoint.Delete()
	if err == nil {
		klog.V(3).Infof("Remote endpoint resource deleted id %s", hnsID)
	}
	return err
}
func (hns hnsV2) getLoadBalancer(endpoints []endpointsInfo, isILB bool, isDSR bool, sourceVip string, vip string, protocol uint16, internalPort uint16, externalPort uint16) (*loadBalancerInfo, error) {
	plists, err := hcn.ListLoadBalancers()
	if err != nil {
		return nil, err
	}

	for _, plist := range plists {
		if len(plist.HostComputeEndpoints) != len(endpoints) {
			continue
		}
		// Validate if input meets any of the policy lists
		lbPortMapping := plist.PortMappings[0]
		if lbPortMapping.Protocol == uint32(protocol) && lbPortMapping.InternalPort == internalPort && lbPortMapping.ExternalPort == externalPort && (lbPortMapping.Flags&1 != 0) == isILB {
			if len(vip) > 0 {
				if len(plist.FrontendVIPs) == 0 || plist.FrontendVIPs[0] != vip {
					continue
				}
			}
			LogJson(plist, "Found existing Hns loadbalancer policy resource", 1)
			return &loadBalancerInfo{
				hnsID: plist.Id,
			}, nil
		}
	}

	var hnsEndpoints []hcn.HostComputeEndpoint
	for _, ep := range endpoints {
		endpoint, err := hcn.GetEndpointByID(ep.hnsID)
		if err != nil {
			return nil, err
		}
		hnsEndpoints = append(hnsEndpoints, *endpoint)
	}

	vips := []string{}
	if len(vip) > 0 {
		vips = append(vips, vip)
	}
	lb, err := hcn.AddLoadBalancer(
		hnsEndpoints,
		isILB,
		isDSR,
		sourceVip,
		vips,
		protocol,
		internalPort,
		externalPort,
	)
	if err != nil {
		return nil, err
	}

	LogJson(lb, "Hns loadbalancer policy resource", 1)

	return &loadBalancerInfo{
		hnsID: lb.Id,
	}, err
}
func (hns hnsV2) deleteLoadBalancer(hnsID string) error {
	lb, err := hcn.GetLoadBalancerByID(hnsID)
	if err != nil {
		// Return silently
		return nil
	}

	err = lb.Delete()
	return err
}
