// SPDX-License-Identifier:Apache-2.0

package main

import (
	"fmt"
	"math/rand"
	"net"
	"testing"

	"go.universe.tf/metallb/internal/allocator"
	"go.universe.tf/metallb/internal/config"
	"go.universe.tf/metallb/internal/k8s/controllers"
	"go.universe.tf/metallb/internal/k8s/epslices"

	"github.com/go-kit/log"
	"github.com/google/go-cmp/cmp"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
)

func diffService(a, b *v1.Service) string {
	// v5 of the k8s client does not correctly compare nil
	// *metav1.Time objects, which svc.ObjectMeta contains. Add
	// some dummy non-nil values to all of in, want, got to work
	// around this until we migrate to v6.
	if a != nil {
		newA := new(v1.Service)
		*newA = *a
		newA.ObjectMeta.DeletionTimestamp = &metav1.Time{}
		a = newA
	}
	if b != nil {
		newB := new(v1.Service)
		*newB = *b
		newB.ObjectMeta.DeletionTimestamp = &metav1.Time{}
		b = newB
	}
	return cmp.Diff(a, b)
}

func ipnet(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func statusAssigned(ips []string) v1.ServiceStatus {
	lbIngressIPs := []v1.LoadBalancerIngress{}
	for _, ip := range ips {
		lbIngressIPs = append(lbIngressIPs, v1.LoadBalancerIngress{IP: ip})
	}
	return v1.ServiceStatus{
		LoadBalancer: v1.LoadBalancerStatus{
			Ingress: lbIngressIPs,
		},
	}
}

// testK8S implements service by recording what the controller wants
// to do to k8s.
type testK8S struct {
	updateService       *v1.Service
	updateServiceStatus *v1.ServiceStatus
	loggedWarning       bool
	t                   *testing.T
}

func (s *testK8S) UpdateStatus(svc *v1.Service) error {
	s.updateServiceStatus = &svc.Status
	return nil
}

func (s *testK8S) Infof(_ *v1.Service, evtType string, msg string, args ...interface{}) {
	s.t.Logf("k8s Info event %q: %s", evtType, fmt.Sprintf(msg, args...))
}

func (s *testK8S) Errorf(_ *v1.Service, evtType string, msg string, args ...interface{}) {
	s.t.Logf("k8s Warning event %q: %s", evtType, fmt.Sprintf(msg, args...))
	s.loggedWarning = true
}

func (s *testK8S) reset() {
	s.updateService = nil
	s.updateServiceStatus = nil
	s.loggedWarning = false
}

func (s *testK8S) gotService(in *v1.Service) *v1.Service {
	if s.updateService == nil && s.updateServiceStatus == nil {
		return nil
	}

	ret := new(v1.Service)
	if in != nil {
		*ret = *in
	}
	if s.updateService != nil {
		*ret = *s.updateService
	}
	if s.updateServiceStatus != nil {
		ret.Status = *s.updateServiceStatus
	}
	return ret
}

func TestControllerMutation(t *testing.T) {
	testSelector, err := labels.Parse("team=metallb")
	if err != nil {
		t.Fatalf("failed to parse test selector")
	}
	k := &testK8S{t: t}
	c := &controller{
		ips:    allocator.New(),
		client: k,
	}
	pools := &config.Pools{ByName: map[string]*config.Pool{
		"pool1": {
			Name:       "pool1",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("1.2.3.0/31")},
		},
		"pool2": {
			Name:       "pool2",
			AutoAssign: false,
			CIDR:       []*net.IPNet{ipnet("3.4.5.6/32")},
		},
		"pool3": {
			Name:       "pool3",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("1000::/127")},
		},
		"pool4": {
			Name:       "pool4",
			AutoAssign: false,
			CIDR:       []*net.IPNet{ipnet("2000::1/128")},
		},
		"pool5": {
			Name:       "pool5",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("1.2.3.0/31"), ipnet("1000::/127")},
		},
		"pool6": {
			Name:       "pool6",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("7.8.9.0/31")},
			ServiceAllocations: &config.ServiceAllocation{Namespaces: sets.New("test-ns1"),
				Priority: 10},
		},
		"pool7": {
			Name:       "pool7",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("10.11.12.0/31")},
			ServiceAllocations: &config.ServiceAllocation{Namespaces: sets.New("test-ns1"),
				Priority: 11},
		},
		"pool8": {
			Name:       "pool8",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("13.14.15.0/31")},
			ServiceAllocations: &config.ServiceAllocation{ServiceSelectors: []labels.Selector{testSelector},
				Priority: 9},
		},
		"pool9": {
			Name:               "pool9",
			AutoAssign:         true,
			CIDR:               []*net.IPNet{ipnet("16.17.18.0/31")},
			ServiceAllocations: &config.ServiceAllocation{Namespaces: sets.New("test-ns2")},
		},
		"pool10": {
			Name:       "pool10",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("19.20.21.0/31")},
			ServiceAllocations: &config.ServiceAllocation{ServiceSelectors: []labels.Selector{testSelector},
				Priority: 8},
		},
		"pool11": {
			Name:       "pool11",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("22.23.24.0/31")},
			ServiceAllocations: &config.ServiceAllocation{Namespaces: sets.New("test-ns1"),
				ServiceSelectors: []labels.Selector{testSelector}, Priority: 8},
		},
		"pool12": {
			Name:       "pool12",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("25.26.27.0/31")},
			ServiceAllocations: &config.ServiceAllocation{Namespaces: sets.New("test-ns1"),
				ServiceSelectors: []labels.Selector{testSelector}, Priority: 5},
		},
	}, ByNamespace: map[string][]string{"test-ns1": {"pool6", "pool7", "pool11", "pool12"}, "test-ns2": {"pool9"}},
		ByServiceSelector: []string{"pool8", "pool10", "pool11", "pool12"},
	}

	l := log.NewNopLogger()

	// For this test, we just set a static config and immediately sync
	// the controller. The mutations around config setting and syncing
	// are tested elsewhere.
	if c.SetPools(l, pools) == controllers.SyncStateError {
		t.Fatalf("SetPools failed")
	}

	// In steady state, every input below should be equivalent to a
	// pure function that reliably produces the same end state
	// regardless of past controller state.
	tests := []struct {
		desc    string
		in      *v1.Service
		want    *v1.Service
		wantErr bool
	}{
		{
			desc: "deleted balancer",
		},

		{
			desc: "simple non-LoadBalancer",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "ClusterIP",
					ClusterIPs: []string{"1.2.3.4"},
				},
			},
		},

		{
			desc: "simple LoadBalancer",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
		},

		{
			desc: "request specific IP",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					ClusterIPs:     []string{"1.2.3.4"},
					Type:           "LoadBalancer",
					LoadBalancerIP: "1.2.3.1",
				},
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					ClusterIPs:     []string{"1.2.3.4"},
					Type:           "LoadBalancer",
					LoadBalancerIP: "1.2.3.1",
				},
				Status: statusAssigned([]string{"1.2.3.1"}),
			},
		},

		{
			desc: "request specific IP via custom annotation",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.1",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.1",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
				Status: statusAssigned([]string{"1.2.3.1"}),
			},
		},

		{
			desc: "request IP from both svc.spec.LoadBalancerIP and custom annotation",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.1",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs:     []string{"1.2.3.4"},
					Type:           "LoadBalancer",
					LoadBalancerIP: "1.2.3.2",
				},
			},
			wantErr: true,
		},

		{
			desc: "request invalid IP",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:           "LoadBalancer",
					LoadBalancerIP: "please sir may I have an IP address thank you",
					ClusterIPs:     []string{"1.2.3.4"},
				},
			},
			wantErr: true,
		},

		{
			desc: "incompatible ip and address pool annotations",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.1",
						AnnotationAddressPool:     "pool2",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
			},
			wantErr: true,
		},

		{
			desc: "request invalid IP via custom annotation",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "please sir may I have an IP address thank you",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
			},
			wantErr: true,
		},

		{
			desc: "request two IPs from same ip family via custom annotation",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.1,1.2.3.2",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
			},
			wantErr: true,
		},

		{
			desc: "request infeasible IP",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:           "LoadBalancer",
					LoadBalancerIP: "1.2.3.4",
					ClusterIPs:     []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"1.2.3.1"}),
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:           "LoadBalancer",
					LoadBalancerIP: "1.2.3.4",
					ClusterIPs:     []string{"1.2.3.4"},
				},
			},
			wantErr: true,
		},

		{
			desc: "request IP from specific pool",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationAddressPool: "pool1",
					},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationAddressPool: "pool1",
					},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
		},

		{
			desc: "switch to a different specific pool",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationAddressPool: "pool2",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationAddressPool: "pool2",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
				Status: statusAssigned([]string{"3.4.5.6"}),
			},
		},

		{
			desc: "unknown pool requested",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationAddressPool: "does-not-exist",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
			},
			wantErr: true,
		},

		{
			desc: "invalid IP assigned",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"2.3.4.5"}),
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
		},

		{
			desc: "invalid ingress state",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: v1.ServiceStatus{
					LoadBalancer: v1.LoadBalancerStatus{
						Ingress: []v1.LoadBalancerIngress{
							{
								Hostname: "foo.bar.local",
							},
							{
								IP: "10.10.10.10",
							},
						},
					},
				},
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
		},

		{
			desc: "former LoadBalancer, now NodePort",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "NodePort",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "NodePort",
					ClusterIPs: []string{"1.2.3.4"},
				},
			},
		},

		{
			desc: "request layer2 service",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:           "LoadBalancer",
					LoadBalancerIP: "3.4.5.6",
					ClusterIPs:     []string{"1.2.3.4"},
				},
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:           "LoadBalancer",
					LoadBalancerIP: "3.4.5.6",
					ClusterIPs:     []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"3.4.5.6"}),
			},
		},

		{
			desc: "Layer2 service with local traffic policy",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:                  "LoadBalancer",
					LoadBalancerIP:        "3.4.5.6",
					ExternalTrafficPolicy: "Local",
					ClusterIPs:            []string{"1.2.3.4"},
				},
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:                  "LoadBalancer",
					LoadBalancerIP:        "3.4.5.6",
					ExternalTrafficPolicy: "Local",
					ClusterIPs:            []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"3.4.5.6"}),
			},
		},

		{
			desc: "No ClusterIP",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type: "LoadBalancer",
				},
			},
			wantErr: false,
		},

		{
			desc: "request IP from wrong ip-family (ipv4)",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:           "LoadBalancer",
					LoadBalancerIP: "1.2.3.1",
					ClusterIPs:     []string{"3000::1"},
				},
			},
			wantErr: true,
		},

		{
			desc: "request IP from wrong ip-family (ipv4) via custom annotation",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.1",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"3000::1"},
					Type:       "LoadBalancer",
				},
			},
			wantErr: true,
		},

		{
			desc: "request IP from wrong ip-family (ipv6)",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:           "LoadBalancer",
					LoadBalancerIP: "1000::",
					ClusterIPs:     []string{"1.2.3.4"},
				},
			},
			wantErr: true,
		},

		{
			desc: "request IP from wrong ip-family (ipv6) via custom annotation",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1000::",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
			},
			wantErr: true,
		},

		{
			desc: "IP from wrong ip-family (ipv6) assigned",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"1000::"}),
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
		},

		{
			desc: "IP from wrong ip-family (ipv4) assigned",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"3000::1"},
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"3000::1"},
				},
				Status: statusAssigned([]string{"1000::"}),
			},
		},
		// dual-stack test cases
		{
			desc: "deleted balancer",
		},
		{
			desc: "simple dual-stack LoadBalancer",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4", "3000::1"},
				},
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4", "3000::1"},
					Type:       "LoadBalancer",
				},
				Status: statusAssigned([]string{"1.2.3.0", "1000::"}),
			},
		},
		{
			desc: "request IPs from specific pool",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationAddressPool: "pool5",
					},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4", "3000::1"},
				},
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationAddressPool: "pool5",
					},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4", "3000::1"},
				},
				Status: statusAssigned([]string{"1.2.3.0", "1000::"}),
			},
		},
		{
			desc: "request specific loadbalancer IP with dual-stack",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					ClusterIPs:     []string{"1.2.3.4", "3000::1"},
					Type:           "LoadBalancer",
					LoadBalancerIP: "1.2.3.1",
				},
			},
			wantErr: true,
		},
		{
			desc: "request specific loadbalancer IPv4 address via custom annotation for dual-stack config",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.1",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4", "3000::1"},
					Type:       "LoadBalancer",
				},
			},
			wantErr: true,
		},
		{
			desc: "request dual-stack loadbalancer IPs via custom annotation",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.0,1000::",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4", "3000::1"},
					Type:       "LoadBalancer",
				},
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.0,1000::",
					},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4", "3000::1"},
				},
				Status: statusAssigned([]string{"1.2.3.0", "1000::"}),
			},
		},
		{
			desc: "request dual-stack loadbalancer with invalid ingress",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"3000::1", "5.6.7.8"},
				},
				Status: v1.ServiceStatus{
					LoadBalancer: v1.LoadBalancerStatus{
						Ingress: []v1.LoadBalancerIngress{
							{
								Hostname: "foo.bar.local",
							},
							{
								IP: "1000::",
							},
						},
					},
				},
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"3000::1", "5.6.7.8"},
				},
				Status: statusAssigned([]string{"1.2.3.0", "1000::"}),
			},
		},
		{
			desc: "request dual-stack loadbalancer IPs via custom annotation in a single stack cluster",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationLoadBalancerIPs: "1.2.3.0,1000::",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIP: "1.2.3.4",
					Type:      "LoadBalancer",
				},
			},
			wantErr: true,
		},
		{
			desc: "request IP for service from namespace specific ip pool",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns1",
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns1",
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"7.8.9.0"}),
			},
		},
		{
			desc: "request IP for service from no priority namespace specific ip pool",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns2",
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns2",
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"16.17.18.0"}),
			},
		},
		{
			desc: "request IP for service from service label specific ip pool",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"team": "metallb"},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"team": "metallb"},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"19.20.21.0"}),
			},
		},
		{
			desc: "request IP for service from ip pool having both namespace and service label",
			in: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns1",
					Labels:    map[string]string{"team": "metallb"},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
			},
			want: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns1",
					Labels:    map[string]string{"team": "metallb"},
				},
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"25.26.27.0"}),
			},
		},
		{
			desc: "simple LoadBalancer, ips already assigned but can't determine family",
			in: &v1.Service{
				Spec: v1.ServiceSpec{
					Type:       "LoadBalancer",
					ClusterIPs: []string{"1.2.3.4"},
				},
				Status: statusAssigned([]string{"1.2.3.0", "1.2.3.1", "1.2.3.2"}),
			},
			want: &v1.Service{
				Spec: v1.ServiceSpec{
					ClusterIPs: []string{"1.2.3.4"},
					Type:       "LoadBalancer",
				},
				Status: statusAssigned([]string{"1.2.3.0"}),
			},
			wantErr: true,
		},
	}

	for i := 0; i < 100; i++ {
		for _, test := range tests {
			t.Run(test.desc, func(t *testing.T) {
				k.reset()

				if c.SetBalancer(l, "test", test.in, epslices.EpsOrSlices{}) == controllers.SyncStateError {
					t.Fatalf("%q: SetBalancer returned error", test.desc)
				}
				if test.wantErr != k.loggedWarning {
					t.Errorf("%q: unexpected loggedWarning value, want %v, got %v", test.desc, test.wantErr, k.loggedWarning)
				}

				gotSvc := k.gotService(test.in)

				switch {
				case test.want == nil && gotSvc != nil:
					t.Errorf("%q: unexpectedly mutated service (-in +out)\n%s", test.desc, diffService(test.in, gotSvc))
				case test.want != nil && gotSvc == nil:
					t.Errorf("%q: did not mutate service, wanted (-in +out)\n%s", test.desc, diffService(test.in, test.want))
				case test.want != nil && gotSvc != nil:
					if diff := diffService(test.want, gotSvc); diff != "" {
						t.Errorf("%q: wrong service mutation (-want +got)\n%s", test.desc, diff)
					}
				}

				if test.want != nil && len(test.want.Status.LoadBalancer.Ingress) > 0 {
					ips := test.want.Status.LoadBalancer.Ingress
					if len(ips) == 0 {
						panic("bad wanted IP in loadbalancer status")
					}
				}
			})
		}

		if t.Failed() {
			// Don't run more test cases if we've already failed, to
			// keep the output readable.
			break
		}

		// Shuffle the input vector, and run again.
		for x := range tests {
			nx := rand.Intn(len(tests) - x)
			tests[x], tests[nx] = tests[nx], tests[x]
		}
		t.Logf("Shuffled test cases")
	}
}

func TestControllerConfig(t *testing.T) {
	k := &testK8S{t: t}
	c := &controller{
		ips:    allocator.New(),
		client: k,
	}

	// Create service that would need an IP allocation

	l := log.NewNopLogger()
	svc := &v1.Service{
		Spec: v1.ServiceSpec{
			Type:       "LoadBalancer",
			ClusterIPs: []string{"1.2.3.4"},
		},
	}
	if c.SetBalancer(l, "test", svc, epslices.EpsOrSlices{}) == controllers.SyncStateError {
		t.Fatalf("SetBalancer failed")
	}

	gotSvc := k.gotService(svc)
	if gotSvc != nil {
		t.Errorf("SetBalancer with no configuration mutated service (-in +out)\n%s", diffService(svc, gotSvc))
	}
	if k.loggedWarning {
		t.Error("SetBalancer with no configuration logged an error")
	}

	// Set an empty config. Balancer should still not do anything to
	// our unallocated service, and return an error to force a
	// retry after sync is complete.
	if c.SetPools(l, &config.Pools{ByName: map[string]*config.Pool{}}) == controllers.SyncStateError {
		t.Fatalf("SetPools with empty config failed")
	}

	gotSvc = k.gotService(svc)
	if gotSvc != nil {
		t.Errorf("unsynced SetBalancer mutated service (-in +out)\n%s", diffService(svc, gotSvc))
	}
	if k.loggedWarning {
		t.Error("unsynced SetBalancer logged an error")
	}

	// Set a config with some IPs. Still no allocation, not synced.
	pools := &config.Pools{ByName: map[string]*config.Pool{
		"default": {
			Name:       "default",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("1.2.3.0/24")},
		},
	}}
	if c.SetPools(l, pools) == controllers.SyncStateError {
		t.Fatalf("SetPools failed")
	}

	gotSvc = k.gotService(svc)
	if gotSvc != nil {
		t.Errorf("unsynced SetBalancer mutated service (-in +out)\n%s", diffService(svc, gotSvc))
	}
	if k.loggedWarning {
		t.Error("unsynced SetBalancer logged an error")
	}

	if c.SetBalancer(l, "test", svc, epslices.EpsOrSlices{}) == controllers.SyncStateError {
		t.Fatalf("SetBalancer failed")
	}

	gotSvc = k.gotService(svc)
	wantSvc := new(v1.Service)
	*wantSvc = *svc
	wantSvc.Status = statusAssigned([]string{"1.2.3.0"})
	if diff := diffService(wantSvc, gotSvc); diff != "" {
		t.Errorf("SetBalancer produced unexpected mutation (-want +got)\n%s", diff)
	}

	// Now that an IP is allocated, removing the IP pool is not allowed.
	if c.SetPools(l, &config.Pools{ByName: map[string]*config.Pool{}}) != controllers.SyncStateError {
		t.Fatalf("SetPools that deletes allocated IPs was accepted")
	}

	// Deleting the config also makes MetalLB sad.
	if c.SetPools(l, nil) != controllers.SyncStateErrorNoRetry {
		t.Fatalf("SetPools that deletes the config was accepted")
	}
}

func TestDeleteRecyclesIP(t *testing.T) {
	k := &testK8S{t: t}
	c := &controller{
		ips:    allocator.New(),
		client: k,
	}

	l := log.NewNopLogger()
	pools := &config.Pools{ByName: map[string]*config.Pool{
		"default": {
			Name:       "default",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("1.2.3.0/32")},
		},
	}}
	if c.SetPools(l, pools) == controllers.SyncStateError {
		t.Fatal("SetPools failed")
	}

	svc1 := &v1.Service{
		Spec: v1.ServiceSpec{
			Type:       "LoadBalancer",
			ClusterIPs: []string{"1.2.3.4"},
		},
	}
	if c.SetBalancer(l, "test", svc1, epslices.EpsOrSlices{}) == controllers.SyncStateError {
		t.Fatal("SetBalancer svc1 failed")
	}
	gotSvc := k.gotService(svc1)
	if gotSvc == nil {
		t.Fatal("Didn't get a balancer for svc1")
	}
	if len(gotSvc.Status.LoadBalancer.Ingress) == 0 || gotSvc.Status.LoadBalancer.Ingress[0].IP != "1.2.3.0" {
		t.Fatal("svc1 didn't get an IP")
	}
	k.reset()

	// Second service should converge correctly, but not allocate an
	// IP because we have none left.
	svc2 := &v1.Service{
		Spec: v1.ServiceSpec{
			Type:       "LoadBalancer",
			ClusterIPs: []string{"1.2.3.4"},
		},
	}
	if c.SetBalancer(l, "test2", svc2, epslices.EpsOrSlices{}) == controllers.SyncStateError {
		t.Fatal("SetBalancer svc2 failed")
	}
	if k.gotService(svc2) != nil {
		t.Fatal("SetBalancer svc2 mutated svc2 even though it should not have allocated")
	}
	k.reset()

	// Deleting the first LB should tell us to reprocess all services.
	if c.SetBalancer(l, "test", nil, epslices.EpsOrSlices{}) != controllers.SyncStateReprocessAll {
		t.Fatal("SetBalancer with nil LB didn't tell us to reprocess all balancers")
	}

	// Setting svc2 should now allocate correctly.
	if c.SetBalancer(l, "test2", svc2, epslices.EpsOrSlices{}) == controllers.SyncStateError {
		t.Fatal("SetBalancer svc2 failed")
	}
	gotSvc = k.gotService(svc2)
	if gotSvc == nil {
		t.Fatal("Didn't get a balancer for svc2")
	}
	if len(gotSvc.Status.LoadBalancer.Ingress) == 0 || gotSvc.Status.LoadBalancer.Ingress[0].IP != "1.2.3.0" {
		t.Fatal("svc2 didn't get an IP")
	}
}
func TestControllerDualStackConfig(t *testing.T) {
	k := &testK8S{t: t}
	c := &controller{
		ips:    allocator.New(),
		client: k,
	}

	l := log.NewNopLogger()
	svc := &v1.Service{
		Spec: v1.ServiceSpec{
			Type:       "LoadBalancer",
			ClusterIPs: []string{"1.2.3.4", "1000::"},
		},
	}
	if c.SetBalancer(l, "test", svc, epslices.EpsOrSlices{}) == controllers.SyncStateError {
		t.Fatalf("SetBalancer failed")
	}

	gotSvc := k.gotService(svc)
	if gotSvc != nil {
		t.Errorf("SetBalancer with no configuration mutated service (-in +out)\n%s", diffService(svc, gotSvc))
	}
	if k.loggedWarning {
		t.Error("SetBalancer with no configuration logged an error")
	}

	// Set an empty config. Balancer should still not do anything to
	// our unallocated service, and return an error to force a
	// retry after sync is complete.
	if c.SetPools(l, &config.Pools{ByName: map[string]*config.Pool{}}) == controllers.SyncStateError {
		t.Fatalf("SetPools with empty config failed")
	}

	gotSvc = k.gotService(svc)
	if gotSvc != nil {
		t.Errorf("unsynced SetBalancer mutated service (-in +out)\n%s", diffService(svc, gotSvc))
	}
	if k.loggedWarning {
		t.Error("unsynced SetBalancer logged an error")
	}

	// Set a config with some IPs. Still no allocation, not synced.
	pools := &config.Pools{ByName: map[string]*config.Pool{
		"default": {
			Name:       "default",
			AutoAssign: true,
			CIDR:       []*net.IPNet{ipnet("1.2.3.0/24"), ipnet("1000::1/127")},
		},
	}}

	if c.SetPools(l, pools) == controllers.SyncStateError {
		t.Fatalf("SetPools failed")
	}

	gotSvc = k.gotService(svc)
	if gotSvc != nil {
		t.Errorf("unsynced SetBalancer mutated service (-in +out)\n%s", diffService(svc, gotSvc))
	}
	if k.loggedWarning {
		t.Error("unsynced SetBalancer logged an error")
	}

	if c.SetBalancer(l, "test", svc, epslices.EpsOrSlices{}) == controllers.SyncStateError {
		t.Fatalf("SetBalancer failed")
	}

	gotSvc = k.gotService(svc)
	wantSvc := new(v1.Service)
	*wantSvc = *svc
	wantSvc.Status = statusAssigned([]string{"1.2.3.0", "1000::"})
	if diff := diffService(wantSvc, gotSvc); diff != "" {
		t.Errorf("SetBalancer produced unexpected mutation (-want +got)\n%s", diff)
	}

	// Now that an IP is allocated, removing the IP pool is not allowed.
	if c.SetPools(l, &config.Pools{ByName: map[string]*config.Pool{}}) != controllers.SyncStateError {
		t.Fatalf("SetPools that deletes allocated IPs was accepted")
	}

	// Deleting the config also makes MetalLB sad.
	if c.SetPools(l, nil) != controllers.SyncStateErrorNoRetry {
		t.Fatalf("SetPools that deletes the config was accepted")
	}
}
