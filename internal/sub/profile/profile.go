// Package profile is what a client profile is made of, whatever core renders it: the
// mihomo, sing-box and Xray renderers all take a Profile.
package profile

import (
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// Profile is what a client profile is made of.
type Profile struct {
	Proxies []model.Proxy // the user's (build.Proxies)
	Nodes   []model.Node  // enabled nodes in display order
	Direct  []string      // hosts that bypass the tunnel: the panel and every node
	// Rules are the admin's own rules (routing.ServedRules) that the app's core parses
	// (RulesFor), before the built-in routing.
	Rules []routing.Rule
	// Routes go after the admin's rules, for the apps that take rule lists (App); Lang
	// names the groups they add.
	Routes routing.Routes
	App    apps.App
	Lang   string
	// Exit is the admin's own entry of the "YouTube without ads" offer, when the app is got
	// one: a config whose rules end on the entry, so YouTube leaves with the entry's Russian
	// address (routing.Routes.YouTubeExit). nil: none, and the profile is as it was.
	Exit *Exit
}

// Exit is where a profile's rules end when the admin routed the "YouTube without ads" offer
// through their own entry node: the node's SOCKS5 the entry listens on, and its name.
type Exit struct {
	Address string
	Port    int
	Name    string
}
