package node

import (
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/sniffer"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/tunnel"
)

// applyDNS sets mihomo's resolver and sniffer to what cfg says. A whole config is applied
// by executor.ApplyConfig, which this engine cannot use: it patches the listeners onto the
// stock tunnel, and the node's listeners must stay on its own (the one that counts traffic
// and keeps devices apart). Proxies and rules are swapped piecewise for the same reason;
// the resolver and the sniffer, being process-wide singletons, are swapped here.
func (e *Engine) applyDNS(cfg *config.Config) {
	if !cfg.DNS.Enable {
		resolver.DefaultResolver = nil
		resolver.DefaultHostMapper = nil
		resolver.DefaultService = nil
		resolver.ProxyServerHostResolver = nil
		resolver.DirectHostResolver = nil
		dns.ReCreateServer("", nil, nil)
	} else {
		c := cfg.DNS
		ipv6 := c.IPv6 && cfg.General.IPv6
		r := dns.NewResolver(dns.Config{
			Main:                 c.NameServer,
			Fallback:             c.Fallback,
			IPv6:                 ipv6,
			IPv6Timeout:          c.IPv6Timeout,
			FallbackIPFilter:     c.FallbackIPFilter,
			FallbackDomainFilter: c.FallbackDomainFilter,
			FallbackLazyQuery:    c.FallbackLazyQuery,
			Default:              c.DefaultNameserver,
			Policy:               c.NameServerPolicy,
			ProxyServer:          c.ProxyServerNameserver,
			ProxyServerPolicy:    c.ProxyServerPolicy,
			DirectServer:         c.DirectNameServer,
			DirectFollowPolicy:   c.DirectFollowPolicy,
			CacheAlgorithm:       c.CacheAlgorithm,
			CacheMaxSize:         c.CacheMaxSize,
		})
		m := dns.NewEnhancer(dns.EnhancerConfig{
			IPv6: ipv6, EnhancedMode: c.EnhancedMode, FakeIPPool: c.FakeIPPool, FakeIPPool6: c.FakeIPPool6,
			FakeIPSkipper: c.FakeIPSkipper, FakeIPTTL: c.FakeIPTTL, UseHosts: c.UseHosts,
		})
		// The answers already given stay valid across the change.
		if old, ok := resolver.DefaultHostMapper.(*dns.ResolverEnhancer); ok && old != nil {
			m.PatchFrom(old)
		}
		s := dns.NewService(r, m)

		resolver.DefaultResolver = r
		resolver.DefaultHostMapper = m
		resolver.DefaultService = s
		resolver.UseSystemHosts = c.UseSystemHosts
		if r.ProxyResolver.Invalid() {
			resolver.ProxyServerHostResolver = r.ProxyResolver
		} else {
			resolver.ProxyServerHostResolver = r.Resolver
		}
		if r.DirectResolver.Invalid() {
			resolver.DirectHostResolver = r.DirectResolver
		} else {
			resolver.DirectHostResolver = r.Resolver
		}
		// No DNS server listens on the node (the config leaves Listen empty): clients keep
		// their own resolvers, the node resolves only what it carries.
		dns.ReCreateServer("", nil, s)
	}

	dispatcher, err := sniffer.NewDispatcher(cfg.Sniffer)
	if err != nil {
		e.log.Warn("sniffer", "err", err)
	}
	tunnel.UpdateSniffer(dispatcher)
	resolver.ResetConnection()
}
