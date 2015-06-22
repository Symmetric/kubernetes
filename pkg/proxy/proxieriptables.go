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

/*
NOTE: this needs to be tested in e2e since it uses iptables for everything.
*/

import (
	"bytes"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/types"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	utilexec "github.com/GoogleCloudPlatform/kubernetes/pkg/util/exec"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/iptables"
	"github.com/golang/glog"
)

// NOTE: It will be tricky to determine the exact version needed.
// Some features will of course be backported in various distros and this could get pretty hairy.
// However iptables-1.4.0 was released 2007-Dec-22 and appears to have every feature we use,
// so this seems prefectly reasonable for now.
const (
	IPTABLES_MIN_MAJOR = 1
	IPTABLES_MIN_MINOR = 4
	IPTABLES_MIN_PATCH = 0
)

func ShouldUseProxierIptables() (bool, error) {
	exec := utilexec.New()
	major, minor, patch, err := iptables.GetIptablesVersion(exec)
	if err != nil {
		return false, err
	}
	return (major >= IPTABLES_MIN_MAJOR) && (minor > IPTABLES_MIN_MINOR) && (patch > IPTABLES_MIN_PATCH), err
}

// This is the same as serviceInfo just without a socket
type serviceInfoIptables struct {
	portal              portal
	protocol            api.Protocol
	proxyPort           int
	timeout             time.Duration
	nodePort            int
	loadBalancerStatus  api.LoadBalancerStatus
	sessionAffinityType api.ServiceAffinity
	stickyMaxAgeMinutes int
	// TODO: do we still want this in ProxierIptables?
	// Deprecated, but required for back-compat (including e2e)
	deprecatedPublicIPs []string
}

// ProxierIptables is an iptables based proxy for connections between a localhost:lport
// and services that provide the actual implementations.
// See Proxier for userspace implementation
// Implements ProxyProvider interface
type ProxierIptables struct {
	loadBalancer  DummyLoadBalancer
	mu            sync.Mutex // protects serviceMap
	serviceMap    map[ServicePortName]*serviceInfoIptables
	hostChains    []string //used so that we can remove the current ones when making new ones
	serviceChains []string //also used for removal. we may want to parse iptables-save instead.
	portMapMutex  sync.Mutex
	portMap       map[portMapKey]ServicePortName
	listenIP      net.IP
	iptables      iptables.Interface
	hostIP        net.IP
	proxyPorts    PortAllocator
}

// NewProxierIptables returns a new ProxierIptables given an address on which to listen.
// Because of the iptables logic, It is assumed that there is only a single
// ProxierIptables active on a machine.
// An error will be returned if the proxier cannot be started due to an invalid
// ListenIP (loopback) or if iptables fails to update or acquire the initial lock.
// Once a proxier is created, it will keep iptables up to date in the background and
// will not terminate if a particular iptables call fails.
func NewProxierIptables(loadBalancer DummyLoadBalancer, listenIP net.IP, iptables iptables.Interface, pr util.PortRange) (*ProxierIptables, error) {
	if listenIP.Equal(localhostIPv4) || listenIP.Equal(localhostIPv6) {
		return nil, ErrProxyOnLocalhost
	}

	hostIP, err := util.ChooseHostInterface()
	if err != nil {
		return nil, fmt.Errorf("failed to select a host interface: %v", err)
	}

	proxyPorts := newPortAllocator(pr)

	glog.V(2).Infof("Setting proxy IP to %v and initializing iptables", hostIP)
	return createProxierIptables(loadBalancer, listenIP, iptables, hostIP, proxyPorts)
}

func createProxierIptables(loadBalancer DummyLoadBalancer, listenIP net.IP, iptables iptables.Interface, hostIP net.IP, proxyPorts PortAllocator) (*ProxierIptables, error) {
	// convenient to pass nil for tests..
	if proxyPorts == nil {
		proxyPorts = newPortAllocator(util.PortRange{})
	}

	return &ProxierIptables{
		loadBalancer: loadBalancer,
		serviceMap:   make(map[ServicePortName]*serviceInfoIptables),
		portMap:      make(map[portMapKey]ServicePortName),
		listenIP:     listenIP,
		iptables:     iptables,
		hostIP:       hostIP,
		proxyPorts:   proxyPorts,
	}, nil
}

func (proxier *ProxierIptables) getServiceInfo(service ServicePortName) (*serviceInfoIptables, bool) {
	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	info, ok := proxier.serviceMap[service]
	return info, ok
}

func (proxier *ProxierIptables) setServiceInfo(service ServicePortName, info *serviceInfoIptables) {
	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	proxier.serviceMap[service] = info
}

func (proxier *ProxierIptables) sameConfig(info *serviceInfoIptables, service *api.Service, port *api.ServicePort) bool {
	if info.protocol != port.Protocol || info.portal.port != port.Port || info.nodePort != port.NodePort {
		return false
	}
	if !info.portal.ip.Equal(net.ParseIP(service.Spec.ClusterIP)) {
		return false
	}
	if !ipsEqual(info.deprecatedPublicIPs, service.Spec.DeprecatedPublicIPs) {
		return false
	}
	if !api.LoadBalancerStatusEqual(&info.loadBalancerStatus, &service.Status.LoadBalancer) {
		return false
	}
	if info.sessionAffinityType != service.Spec.SessionAffinity {
		return false
	}
	return true
}

// TODO: what should this be?
const syncIntervalIptables = 5 * time.Second

// SyncLoop runs periodic work.  This is expected to run as a goroutine or as the main loop of the app.  It does not return.
func (proxier *ProxierIptables) SyncLoop() {
	//TODO: should the sync interval differ from Proxier's ?
	t := time.NewTicker(syncIntervalIptables)
	defer t.Stop()
	for {
		<-t.C
		glog.V(6).Infof("Periodic sync")
		if err := iptablesInit(proxier.iptables); err != nil {
			glog.Errorf("Failed to ensure iptables: %v", err)
		}
		if err := proxier.syncProxyRules(); err != nil {
			glog.Errorf("Failed to sync iptables rules: %v", err)
		}
	}
}

// OnUpdate tracks the active set of service proxies.
// They will be synchronized using syncProxyRules()
func (proxier *ProxierIptables) OnUpdate(services []api.Service) {
	glog.V(4).Infof("Received update notice: %+v", services)
	activeServices := make(map[ServicePortName]bool) // use a map as a set

	for i := range services {
		service := &services[i]

		// if ClusterIP is "None" or empty, skip proxying
		if !api.IsServiceIPSet(service) {
			glog.V(3).Infof("Skipping service %s due to portal IP = %q", types.NamespacedName{service.Namespace, service.Name}, service.Spec.ClusterIP)
			continue
		}

		for i := range service.Spec.Ports {
			servicePort := &service.Spec.Ports[i]

			serviceName := ServicePortName{types.NamespacedName{service.Namespace, service.Name}, servicePort.Name}
			activeServices[serviceName] = true
			serviceIP := net.ParseIP(service.Spec.ClusterIP)
			info, exists := proxier.getServiceInfo(serviceName)
			if exists && proxier.sameConfig(info, service, servicePort) {
				// Nothing changed.
				continue
			}
			if exists {
				//Something changed.
				glog.V(4).Infof("Something changed for service %q: stopping it", serviceName)
				err := proxier.removeService(serviceName, info)
				if err != nil {
					glog.Errorf("Failed to remove service %q: %v", serviceName, err)
				}
			}

			glog.V(1).Infof("Adding new service %q at %s:%d/%s", serviceName, serviceIP, servicePort.Port, servicePort.Protocol)
			info, err := proxier.addServiceOnPort(serviceName, servicePort.Protocol, 0, udpIdleTimeout)
			if err != nil {
				glog.Errorf("Failed to add service for for %q: %v", serviceName, err)
				continue
			}
			info.portal.ip = serviceIP
			info.portal.port = servicePort.Port
			info.deprecatedPublicIPs = service.Spec.DeprecatedPublicIPs
			// Deep-copy in case the service instance changes
			info.loadBalancerStatus = *api.LoadBalancerStatusDeepCopy(&service.Status.LoadBalancer)
			info.nodePort = servicePort.NodePort
			info.sessionAffinityType = service.Spec.SessionAffinity

			glog.V(4).Infof("info: %+v", info)
			proxier.loadBalancer.NewService(serviceName, info.sessionAffinityType)
		}
	}

	func() {
		proxier.mu.Lock()
		defer proxier.mu.Unlock()
		for name, info := range proxier.serviceMap {
			if !activeServices[name] {
				glog.V(1).Infof("Removing service %q", name)
				proxier.removeServiceInternal(name, info)
			}
		}
	}()

	glog.V(6).Infof("Periodic sync")
	if err := iptablesInit(proxier.iptables); err != nil {
		glog.Errorf("Failed to ensure iptables: %v", err)
	}
	// this locks proxier.mu, hence the closure above
	if err := proxier.syncProxyRules(); err != nil {
		glog.Errorf("Failed to sync iptables rules: %v", err)
	}
}

const (
	fmtChain  = ":%s - [0:0]\n"
	fmtFlush  = "-F %s\n"
	fmtDelete = "-X %s\n"
)

// TODO: sticky sessions
func (proxier *ProxierIptables) syncProxyRules() error {
	glog.V(2).Infof("Syncing iptables rules.")

	proxier.mu.Lock()
	defer proxier.mu.Unlock()

	// for first line and chains
	var rules bytes.Buffer
	// for the actual rules (which should be after the list of chains)
	var rulesEnd bytes.Buffer
	rules.WriteString("*nat\n")
	rules.WriteString(fmt.Sprintf(fmtChain, iptablesHostPortalChain))
	rules.WriteString(fmt.Sprintf(fmtChain, iptablesContainerPortalChain))
	rules.WriteString(fmt.Sprintf(fmtChain, iptablesHostNodePortChain))
	rules.WriteString(fmt.Sprintf(fmtChain, iptablesContainerNodePortChain))

	//flush the chains
	rulesEnd.WriteString(fmt.Sprintf(fmtFlush, iptablesHostPortalChain))
	rulesEnd.WriteString(fmt.Sprintf(fmtFlush, iptablesContainerPortalChain))
	rulesEnd.WriteString(fmt.Sprintf(fmtFlush, iptablesHostNodePortChain))
	rulesEnd.WriteString(fmt.Sprintf(fmtFlush, iptablesContainerNodePortChain))

	// Flush old host & service chains
	for _, chain := range proxier.hostChains {
		rulesEnd.WriteString(fmt.Sprintf(fmtFlush, chain))
	}
	for _, chain := range proxier.serviceChains {
		rulesEnd.WriteString(fmt.Sprintf(fmtFlush, chain))
	}
	newHostChains := []string{}
	newServiceChains := []string{}

	// Used for naming chains
	// Iptables on fedora has a 28 char limit on chain names, so we do KUBE-SERVICE-i,
	// and for each endpoint KUBE-SERVICE-i_j
	i := 0
	//Build rules for services
	for name, info := range proxier.serviceMap {

		if info.proxyPort == 0 {
			port, err := proxier.proxyPorts.AllocateNext()
			// TODO: should we handle this differently?
			if err != nil || port == 0 {
				if err != nil {
					glog.Errorf("Failed to allocate proxy port for service %q: %v, falling back on getting a port from a socket.", name, err)
				}
				if port == 0 {
					glog.Errorf("Proxy port set to zero for service: %v, falling back on getting a port from a socket.", name)
				}
				port, err = getRandomPortFromSocket(info.protocol, proxier.listenIP)
				if err != nil {
					glog.Errorf("Failed to get a random port from opening a socket: %v", err)
				}
			}
			if port == 0 {
				glog.Errorf("Proxy port still zero for service: %v, skipping.", name)
				continue
			}
			info.proxyPort = port
		}

		protocol := strings.ToLower((string)(info.protocol))

		svcNum := i
		svcChain := "KUBE-SERVICE-" + strconv.Itoa(svcNum)
		// Increment chain number
		i++
		// Create chain
		rules.WriteString(fmt.Sprintf(fmtChain, svcChain))
		// Flush chain
		rulesEnd.WriteString(fmt.Sprintf(fmtFlush, svcChain))
		// nodePort
		if info.nodePort != 0 {
			err := proxier.claimPort(info.nodePort, info.protocol, name)
			if err != nil {
				glog.Errorf("Failed to claim nodePort: %v", err)
			} else {
				proxyIP := proxier.listenIP
				// Handle traffic from containers.
				rulesEnd.WriteString(fmt.Sprintf("-A %s -p %s -m %s --dport %d", iptablesContainerNodePortChain, protocol, protocol, info.nodePort))
				if proxyIP.Equal(zeroIPv4) || proxyIP.Equal(zeroIPv6) {
					// TODO: Can we REDIRECT with IPv6?
					rulesEnd.WriteString(fmt.Sprintf(" -j REDIRECT --to-ports %d\n", info.proxyPort))
				} else {
					// TODO: Can we DNAT with IPv6?
					rulesEnd.WriteString(fmt.Sprintf(" -j DNAT --to-destination %s:%d\n"+proxyIP.String(), info.proxyPort))
				}
				// Handle traffic from the host.
				if proxyIP.Equal(zeroIPv4) || proxyIP.Equal(zeroIPv6) {
					proxyIP = proxier.hostIP
				}
				rulesEnd.WriteString(fmt.Sprintf("-A %s -p %s -m %s --dport %d -j DNAT --to-destination %s:%d\n", iptablesHostNodePortChain, protocol, protocol, info.nodePort, proxyIP.String(), info.proxyPort))
			}
		}

		// Get endpoints
		lbInfo, exists := proxier.loadBalancer.GetService(name)
		hosts := make([]string, 0)
		hostChains := make([]string, 0)
		if exists {
			//glog.V(1).Infof("Got lbInfo for %s, len(endpoints): %d", name, len(lbInfo.endpoints))
			for i, ep := range lbInfo.endpoints {
				hosts = append(hosts, ep)
				hostChains = append(hostChains, svcChain+"_"+strconv.Itoa(i))
			}
		} else {
			//glog.V(1).Infof("Failed to get lbInfo for %s", name)
		}

		// Ensure we know what chains to flush/remove next time we generate the rules
		newHostChains = append(newHostChains, hostChains...)
		newServiceChains = append(newServiceChains, svcChain)

		n := len(hostChains)
		for i, hostChain := range hostChains {
			// Create chain
			rules.WriteString(fmt.Sprintf(fmtChain, hostChain))
			// Flush chain
			rulesEnd.WriteString(fmt.Sprintf(fmtFlush, hostChain))
			// Sticky session
			if info.sessionAffinityType == api.ServiceAffinityClientIP {
				rulesEnd.WriteString(fmt.Sprintf("-A %s -m recent --name %s --rcheck --seconds %d --reap -j %s\n", svcChain, hostChain, info.stickyMaxAgeMinutes*60, hostChain))
			}
			// Roughly round robin statistically if we have more than one host
			if n > 1 {
				rulesEnd.WriteString(fmt.Sprintf("-A %s -m statistic --mode random --probaibility %f -j %s\n", svcChain, 1.0/(n-i), hostChain))
			} else {
				rulesEnd.WriteString(fmt.Sprintf("-A %s -j %s\n", svcChain, hostChain))
			}
			// proxy
			rulesEnd.WriteString(fmt.Sprintf("-A %s -m recent --name %s --set -j DNAT -p %s --to-destination %s\n", hostChain, hostChain, protocol, hosts[i]))
		}
		// proxy
		rulesEnd.WriteString(fmt.Sprintf("-A %s -m comment --comment \"portal for %s\" -d %s/32 -m state --state NEW -p %s -m %s --dport %d -j %s\n", iptablesContainerPortalChain, name.String(), info.portal.ip.String(), protocol, protocol, info.portal.port, svcChain))
		rulesEnd.WriteString(fmt.Sprintf("-A %s -m comment --comment \"portal for %s\" -d %s/32 -m state --state NEW -p %s -m %s --dport %d -j %s\n", iptablesHostPortalChain, name.String(), info.portal.ip.String(), protocol, protocol, info.portal.port, svcChain))

		for _, publicIP := range info.deprecatedPublicIPs {
			rulesEnd.WriteString(fmt.Sprintf("-A %s -m comment --comment \"portal for %s\" -d %s/32 -m state --state NEW -p %s -m %s --dport %d -j %s\n", iptablesContainerPortalChain, name.String(), publicIP, protocol, protocol, info.portal.port, svcChain))
			rulesEnd.WriteString(fmt.Sprintf("-A %s -m comment --comment \"portal for %s\" -d %s/32 -m state --state NEW -p %s -m %s --dport %d -j %s\n", iptablesHostPortalChain, name.String(), publicIP, protocol, protocol, info.portal.port, svcChain))
		}

		for _, ingress := range info.loadBalancerStatus.Ingress {
			if ingress.IP != "" {
				rulesEnd.WriteString(fmt.Sprintf("-A %s -m comment --comment \"portal for %s\" -d %s/32 -m state --state NEW -p %s -m %s --dport %d -j %s\n", iptablesContainerPortalChain, name.String(), ingress.IP, protocol, protocol, info.portal.port, svcChain))
				rulesEnd.WriteString(fmt.Sprintf("-A %s -m comment --comment \"portal for %s\" -d %s/32 -m state --state NEW -p %s -m %s --dport %d -j %s\n", iptablesHostPortalChain, name.String(), ingress.IP, protocol, protocol, info.portal.port, svcChain))
			}
		}

	}

	//Delete chains no longer in use:
	activeChains := make(map[string]bool) // use a map as a set
	for _, chain := range newHostChains {
		activeChains[chain] = true
	}
	for _, chain := range newServiceChains {
		activeChains[chain] = true
	}

	for _, chain := range proxier.serviceChains {
		if !activeChains[chain] {
			rulesEnd.WriteString(fmt.Sprintf(fmtDelete, chain))
		}
	}
	for _, chain := range proxier.hostChains {
		if !activeChains[chain] {
			rulesEnd.WriteString(fmt.Sprintf(fmtDelete, chain))
		}
	}

	proxier.hostChains = newHostChains
	proxier.serviceChains = newServiceChains

	rulesEnd.WriteString("COMMIT\n")
	lines := append(rules.Bytes(), rulesEnd.Bytes()...)

	glog.V(1).Infof("Syncing rule: %s", lines)

	// NOTE: --noflush is used so we don't flush all rules.
	// we manually flush the rules we need to flush.
	err := iptables.Restore([]string{"--noflush"}, lines)
	return err
}

// Temporary hack to deal with when the proxy allocator gives us 0 as the port
// we can fall back on opening and closing a socket with the port set to 0
// and getting the port from that chosen by the OS in theory.
func getRandomPortFromSocket(protocol api.Protocol, ip net.IP) (int, error) {
	host := ip.String()
	switch strings.ToUpper(string(protocol)) {
	case "TCP":
		sock, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(0)))
		if err != nil {
			return 0, err
		}
		_, portStr, err := net.SplitHostPort(sock.Addr().String())
		if err != nil {
			sock.Close()
			return 0, err
		}
		portNum, err := strconv.Atoi(portStr)
		if err != nil {
			sock.Close()
			return 0, err
		}
		sock.Close()
		return portNum, nil
	case "UDP":
		addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(0)))
		if err != nil {
			return 0, err
		}
		sock, err := net.ListenUDP("udp", addr)
		if err != nil {
			return 0, err
		}
		_, portStr, err := net.SplitHostPort(sock.LocalAddr().String())
		if err != nil {
			sock.Close()
			return 0, err
		}
		portNum, err := strconv.Atoi(portStr)
		if err != nil {
			sock.Close()
			return 0, err
		}
		sock.Close()
		return portNum, nil
	}
	return 0, fmt.Errorf("unknown protocol %q", protocol)
}

// Assumes proxier.mu is held
func (proxier *ProxierIptables) removeServiceInternal(name ServicePortName, info *serviceInfoIptables) error {
	delete(proxier.serviceMap, name)
	proxier.proxyPorts.Release(info.proxyPort)
	err := proxier.releasePort(info.nodePort, info.protocol, name)
	return err
}

func (proxier *ProxierIptables) removeService(name ServicePortName, info *serviceInfoIptables) error {
	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	return proxier.removeServiceInternal(name, info)
}

// Pass proxyPort=0 to allocate a random port.
func (proxier *ProxierIptables) addServiceOnPort(service ServicePortName, protocol api.Protocol, proxyPort int, timeout time.Duration) (*serviceInfoIptables, error) {
	portNum := proxyPort
	if portNum == 0 {
		port, err := proxier.proxyPorts.AllocateNext()
		// TODO: how do we handle this?
		if err != nil || port == 0 {
			if err != nil {
				glog.Errorf("Failed to allocate proxy port for service %q: %v, falling back on getting a port from a socket.", service, err)
			}
			if port == 0 {
				glog.Errorf("Proxy port set to zero for service: %v, falling back on getting a port from a socket.", service)
			}
			port, err = getRandomPortFromSocket(protocol, proxier.listenIP)
			if err != nil {
				glog.Errorf("Failed to get a random port from opening a socket: %v", err)
			}
		}
		if port == 0 {
			glog.Errorf("Proxy port still zero for service! (%v)", service)
		}
		portNum = port
	}
	si := &serviceInfoIptables{
		proxyPort:           portNum,
		protocol:            protocol,
		timeout:             timeout,
		sessionAffinityType: api.ServiceAffinityNone, // default
		stickyMaxAgeMinutes: 180,                     // TODO: paramaterize this in the API.
	}
	proxier.setServiceInfo(service, si)
	return si, nil
}

// Marks a port as being owned by a particular service, or returns error if already claimed.
// Idempotent: reclaiming with the same owner is not an error
func (proxier *ProxierIptables) claimPort(port int, protocol api.Protocol, owner ServicePortName) error {
	proxier.portMapMutex.Lock()
	defer proxier.portMapMutex.Unlock()

	// TODO: We could pre-populate some reserved ports into portMap and/or blacklist some well-known ports

	key := portMapKey{port: port, protocol: protocol}
	existing, found := proxier.portMap[key]
	if !found {
		proxier.portMap[key] = owner
		return nil
	}
	if existing == owner {
		// We are idempotent
		return nil
	}
	return fmt.Errorf("Port conflict detected on port %v.  %v vs %v", key, owner, existing)
}

// Release a claim on a port.  Returns an error if the owner does not match the claim.
// Tolerates release on an unclaimed port, to simplify.
func (proxier *ProxierIptables) releasePort(port int, protocol api.Protocol, owner ServicePortName) error {
	proxier.portMapMutex.Lock()
	defer proxier.portMapMutex.Unlock()

	key := portMapKey{port: port, protocol: protocol}
	existing, found := proxier.portMap[key]
	if !found {
		// We tolerate this, it happens if we are cleaning up a failed allocation
		glog.Infof("Ignoring release on unowned port: %v", key)
		return nil
	}
	if existing != owner {
		return fmt.Errorf("Port conflict detected on port %v (unowned unlock).  %v vs %v", key, owner, existing)
	}
	delete(proxier.portMap, key)
	return nil
}
