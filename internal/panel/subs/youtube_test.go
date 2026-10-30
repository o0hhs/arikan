package subs

import "testing"

// The card on the subscription page follows the offer exactly: YouTube through one node
// and the ad list blocked, both set by the panel's one switch.
func TestYouTubeAdFree(t *testing.T) {
	cases := []struct {
		name string
		r    Routes
		want bool
	}{
		{"the offer is on", Routes{Services: map[string]string{"youtube": "node:2", "ads": TargetBlock}}, true},
		{"youtube through the vpn group", Routes{Services: map[string]string{"youtube": TargetVPN, "ads": TargetBlock}}, false},
		{"no ad block", Routes{Services: map[string]string{"youtube": "node:2"}}, false},
		{"no youtube route", Routes{Services: map[string]string{"ads": TargetBlock}}, false},
		{"nothing set", Routes{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.r.YouTubeAdFree(); got != c.want {
				t.Fatalf("YouTubeAdFree() = %v, want %v", got, c.want)
			}
		})
	}
}
