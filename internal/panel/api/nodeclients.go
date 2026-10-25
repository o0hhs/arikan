package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
)

// Who went through which node: the per-node half of the traffic, by user. Both cards read
// the same rows — a node's clients and a user's nodes are the two sides of one table.

// NodeClientView is one user on a node: what they carried over it and when they were last
// seen there.
type NodeClientView struct {
	UserID    int64      `json:"user_id"`
	Name      string     `json:"name"`
	Up        int64      `json:"up"`
	Down      int64      `json:"down"`
	Bytes     int64      `json:"bytes" doc:"up + down"`
	Online    bool       `json:"online" doc:"Подключён к этой ноде прямо сейчас"`
	FirstSeen *time.Time `json:"first_seen" doc:"Когда пользователя впервые увидели на ноде; null — время неизвестно"`
	LastSeen  *time.Time `json:"last_seen" doc:"Когда его в последний раз видели на ноде; null — время неизвестно"`
}

type nodeClientsOutput struct {
	Body struct {
		Items []NodeClientView `json:"items" doc:"Клиенты ноды, больше всех трафика сверху"`
		Total int64            `json:"total" doc:"Сколько всего клиентов у ноды; items может быть короче"`
	}
}

type nodeClientsInput struct {
	ID    int64 `path:"id" minimum:"1"`
	Limit int   `query:"limit" minimum:"1" maximum:"1000" default:"500" doc:"Сколько показать; всего клиентов в total"`
}

// UserNodeView is one node of a user: what they carried over it and when they were last
// seen there.
type UserNodeView struct {
	NodeID     int64      `json:"node_id"`
	Name       string     `json:"name"`
	PublicName string     `json:"public_name" doc:"Имя ноды в подписке; пусто — берётся name"`
	Up         int64      `json:"up"`
	Down       int64      `json:"down"`
	Bytes      int64      `json:"bytes" doc:"up + down"`
	Online     bool       `json:"online" doc:"Пользователь подключён к этой ноде прямо сейчас"`
	FirstSeen  *time.Time `json:"first_seen" doc:"Когда пользователя впервые увидели на ноде; null — время неизвестно"`
	LastSeen   *time.Time `json:"last_seen" doc:"Когда его в последний раз видели на ноде; null — время неизвестно"`
}

type userNodesOutput struct {
	Body struct {
		Items []UserNodeView `json:"items" doc:"Ноды пользователя, больше всех трафика сверху"`
	}
}

func (h *handlers) registerNodeClients() {
	huma.Register(h.api, huma.Operation{OperationID: "node-clients", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/clients", Summary: "Клиенты ноды",
		Description: "Кто ходил через ноду, когда его видели в последний раз и сколько он через неё унёс. Считается с версии, где это появилось: прошлое не восстанавливается.",
		Tags:        []string{"node"}}, h.nodeClients)
	huma.Register(h.api, huma.Operation{OperationID: "user-nodes", Method: http.MethodGet, Path: "/api/v1/users/{id}/nodes", Summary: "Ноды пользователя",
		Description: "Через какие ноды ходил пользователь, сколько унёс через каждую и когда его там видели в последний раз.",
		Tags:        []string{"users"}}, h.userNodes)
	huma.Register(h.api, huma.Operation{OperationID: "user-protocols", Method: http.MethodGet, Path: "/api/v1/users/{id}/protocols", Summary: "Ноды и протоколы пользователя",
		Description: "Какая нода каким протоколом впускала устройства подписки за последний час: у каждой ноды её трафик для этой подписки и протоколы, которыми через неё ходили.",
		Tags:        []string{"users"}}, h.userProtocols)
}

// nodeOnline is one node's live view by slot name: empty without nodes or for one that is
// not running.
func (h *handlers) nodeOnline(id int64) map[string]nodeapi.Online {
	if h.d.NodeOnline == nil {
		return nil
	}
	return h.d.NodeOnline(id)
}

// onlineUsers is who of a node's slots is live right now, by user id.
func (h *handlers) nodeOnlineUsers(ctx context.Context, id int64) (map[int64]bool, error) {
	online := h.nodeOnline(id)
	if len(online) == 0 {
		return nil, nil
	}
	slots := make([]string, 0, len(online))
	for s := range online {
		slots = append(slots, s)
	}
	rows, err := h.d.Store.Q.SlotOwners(ctx, slots)
	if err != nil {
		return nil, err
	}
	live := make(map[int64]bool, len(rows))
	for _, r := range rows {
		live[r.UserID] = true
	}
	return live, nil
}

// ptrUnix is a unix time; the zero (or negative) is no time known, not 1970.
func ptrUnix(v int64) *time.Time {
	if v <= 0 {
		return nil
	}
	t := time.Unix(v, 0).UTC()
	return &t
}

func (h *handlers) nodeClients(ctx context.Context, in *nodeClientsInput) (*nodeClientsOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	out := &nodeClientsOutput{}
	out.Body.Items = []NodeClientView{}
	rows, err := h.d.Store.Q.ListNodeClients(ctx, db.ListNodeClientsParams{NodeID: in.ID, Lim: int32(in.Limit)})
	if err != nil {
		return nil, err
	}
	if out.Body.Total, err = h.d.Store.Q.CountNodeClients(ctx, in.ID); err != nil {
		return nil, err
	}
	live, err := h.nodeOnlineUsers(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Body.Items = append(out.Body.Items, NodeClientView{
			UserID: r.UserID, Name: r.Name, Up: r.Up, Down: r.Down, Bytes: r.Up + r.Down,
			Online: live[r.UserID], FirstSeen: ptrUnix(r.FirstSeen), LastSeen: ptrUnix(r.LastSeen),
		})
	}
	return out, nil
}

// UserProtocolView is one protocol on one node: the preset, how many devices of the
// subscription reached it there, and when it last let one in.
type UserProtocolView struct {
	Preset   string     `json:"preset" doc:"Пресет протокола; custom — своя настройка входящего"`
	Title    string     `json:"title" doc:"Название протокола для человека"`
	LastSeen *time.Time `json:"last_seen" doc:"Когда протокол в последний раз впустил устройство этой подписки на этой ноде"`
	Devices  int        `json:"devices" doc:"Сколько устройств подписки им пользовались на этой ноде"`
}

// UserProtocolNodeView is one node the subscription went through: what it carried for it,
// and which protocols admitted its devices there lately.
type UserProtocolNodeView struct {
	NodeID     int64              `json:"node_id"`
	Name       string             `json:"name"`
	PublicName string             `json:"public_name" doc:"Имя ноды в подписке; пусто — берётся name"`
	Up         int64              `json:"up" doc:"Сколько подписка отдала через эту ноду всего"`
	Down       int64              `json:"down" doc:"Сколько получила"`
	Bytes      int64              `json:"bytes" doc:"up + down"`
	FirstSeen  *time.Time         `json:"first_seen" doc:"Когда подписку впервые увидели на этой ноде; null — время неизвестно"`
	LastSeen   *time.Time         `json:"last_seen" doc:"Когда её в последний раз видели на этой ноде; null — время неизвестно"`
	Protocols  []UserProtocolView `json:"protocols" doc:"Протоколы на этой ноде, недавние сверху; пусто — за окно активности нет"`
}

type userProtocolsOutput struct {
	Body struct {
		Nodes []UserProtocolNodeView `json:"nodes" doc:"Ноды подписки: больше трафика — выше; нода без активности за окно тоже здесь"`
		// WindowSeconds is how far back a node remembers: what is older is not a statistic
		// yet, only the last activity.
		WindowSeconds int `json:"window_seconds" doc:"Окно, за которое ноды помнят активность"`
	}
}

// userProtocols says which node with which protocol the subscription actually used lately:
// each node keeps, per device, the inbound that last admitted it (an hour of memory,
// internal/node's activityKeep), the slots are the subscription's own, and the node's total
// traffic comes from the per-node counters. A protocol is one preset on one node.
func (h *handlers) userProtocols(ctx context.Context, in *userIDInput) (*userProtocolsOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	out := &userProtocolsOutput{}
	out.Body.Nodes = []UserProtocolNodeView{}
	out.Body.WindowSeconds = 3600
	slots, err := h.d.Store.Q.ListUserSlots(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	// Every node the subscription went through, by traffic: the card shows it even where
	// the last hour had no activity.
	perNode := map[int64]*UserProtocolNodeView{}
	if rows, err := h.d.Store.Q.ListUserNodes(ctx, in.ID); err != nil {
		return nil, err
	} else {
		for _, r := range rows {
			n := &UserProtocolNodeView{NodeID: r.NodeID, Name: r.Name, PublicName: r.PublicName, Up: r.Up, Down: r.Down, Bytes: r.Up + r.Down,
				FirstSeen: ptrUnix(r.FirstSeen), LastSeen: ptrUnix(r.LastSeen), Protocols: []UserProtocolView{}}
			perNode[n.NodeID] = n
		}
	}
	if h.d.Nodes == nil || len(slots) == 0 {
		for _, n := range perNode {
			out.Body.Nodes = append(out.Body.Nodes, *n)
		}
		sortNodes(out.Body.Nodes)
		return out, nil
	}
	mine := make(map[string]bool, len(slots))
	for _, s := range slots {
		mine[s] = true
	}
	inbounds, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]db.Inbound, len(inbounds))
	for _, i := range inbounds {
		byName[fmt.Sprintf("%d/%s", i.NodeID, i.Name)] = i
	}
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	type used struct {
		title   string
		last    int64
		devices map[string]bool
	}
	got := map[int64]map[string]*used{} // node → preset → use
	for _, n := range nodes {
		act, err := h.d.Nodes.Activity(ctx, n.ID)
		if err != nil {
			continue // a node that is off answers nothing; the rest still tell theirs
		}
		for _, c := range act.Clients {
			if !mine[c.Slot] {
				continue
			}
			byPreset := got[n.ID]
			if byPreset == nil {
				byPreset = map[string]*used{}
				got[n.ID] = byPreset
			}
			for name, ts := range c.Seen {
				preset, title := "custom", name
				if i, ok := byName[fmt.Sprintf("%d/%s", n.ID, name)]; ok {
					preset = i.Preset
					if i.DisplayName != "" {
						title = i.DisplayName
					}
				}
				if p, ok := presets.Get(preset); ok {
					title = p.Title
				}
				u := byPreset[preset]
				if u == nil {
					u = &used{title: title, devices: map[string]bool{}}
					byPreset[preset] = u
				}
				if ts > u.last {
					u.last = ts
				}
				u.devices[c.IP] = true
			}
		}
	}
	for nodeID, byPreset := range got {
		n := perNode[nodeID]
		if n == nil {
			// Activity without a stored byte yet (a node seen a moment ago): still the node.
			for _, dbn := range nodes {
				if dbn.ID == nodeID {
					n = &UserProtocolNodeView{NodeID: dbn.ID, Name: dbn.Name, PublicName: dbn.PublicName, Protocols: []UserProtocolView{}}
					perNode[nodeID] = n
					break
				}
			}
			if n == nil {
				continue
			}
		}
		for preset, u := range byPreset {
			n.Protocols = append(n.Protocols, UserProtocolView{Preset: preset, Title: u.title, LastSeen: ptrUnix(u.last), Devices: len(u.devices)})
		}
		sort.Slice(n.Protocols, func(i, j int) bool {
			a, b := n.Protocols[i], n.Protocols[j]
			if a.LastSeen != nil && b.LastSeen != nil && !a.LastSeen.Equal(*b.LastSeen) {
				return a.LastSeen.After(*b.LastSeen)
			}
			return a.Preset < b.Preset
		})
	}
	for _, n := range perNode {
		out.Body.Nodes = append(out.Body.Nodes, *n)
	}
	sortNodes(out.Body.Nodes)
	return out, nil
}

// sortNodes: the node that carried most first; ties by name.
func sortNodes(list []UserProtocolNodeView) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Bytes != list[j].Bytes {
			return list[i].Bytes > list[j].Bytes
		}
		return list[i].Name < list[j].Name
	})
}

func (h *handlers) userNodes(ctx context.Context, in *userIDInput) (*userNodesOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	out := &userNodesOutput{}
	out.Body.Items = []UserNodeView{}
	rows, err := h.d.Store.Q.ListUserNodes(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return out, nil
	}
	// A user's slots are the same on every node; each node's live view says where the user
	// is connected now.
	slots, err := h.d.Store.Q.ListUserSlots(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		live := false
		if on := h.nodeOnline(r.NodeID); len(on) > 0 {
			for _, s := range slots {
				if _, ok := on[s]; ok {
					live = true
					break
				}
			}
		}
		out.Body.Items = append(out.Body.Items, UserNodeView{
			NodeID: r.NodeID, Name: r.Name, PublicName: r.PublicName, Up: r.Up, Down: r.Down, Bytes: r.Up + r.Down,
			Online: live, FirstSeen: ptrUnix(r.FirstSeen), LastSeen: ptrUnix(r.LastSeen),
		})
	}
	return out, nil
}
