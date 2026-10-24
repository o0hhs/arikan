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
	huma.Register(h.api, huma.Operation{OperationID: "user-protocols", Method: http.MethodGet, Path: "/api/v1/users/{id}/protocols", Summary: "Протоколы пользователя",
		Description: "Какими протоколами подписка пользовалась за последний час: ноды помнят, какой inbound впустил каждое устройство. Не сумма трафика, а то, чем человек реально подключается.",
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

// UserProtocolView is one protocol a subscription used lately: the preset, how many of its
// devices reached it, and when it last let one in.
type UserProtocolView struct {
	Preset   string     `json:"preset" doc:"Пресет протокола; custom — своя настройка входящего"`
	Title    string     `json:"title" doc:"Название протокола для человека"`
	LastSeen *time.Time `json:"last_seen" doc:"Когда протокол в последний раз впустил устройство этой подписки"`
	Devices  int        `json:"devices" doc:"Сколько устройств подписки им пользовались"`
	Nodes    int        `json:"nodes" doc:"На скольких нодах"`
}

type userProtocolsOutput struct {
	Body struct {
		Items []UserProtocolView `json:"items" doc:"Протоколы, недавние сверху"`
		// WindowSeconds is how far back a node remembers: what is older is not a statistic
		// yet, only the last activity.
		WindowSeconds int `json:"window_seconds" doc:"Окно, за которое ноды помнят активность"`
	}
}

// userProtocols says which protocols the subscription actually used lately: each node keeps,
// per device, the inbound that last admitted it (an hour of memory, internal/node's
// activityKeep), and the slots are the subscription's own.
func (h *handlers) userProtocols(ctx context.Context, in *userIDInput) (*userProtocolsOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	out := &userProtocolsOutput{}
	out.Body.Items = []UserProtocolView{}
	out.Body.WindowSeconds = 3600
	if h.d.Nodes == nil {
		return out, nil
	}
	slots, err := h.d.Store.Q.ListUserSlots(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if len(slots) == 0 {
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
	type used struct {
		title   string
		last    int64
		devices map[string]bool
		nodes   map[int64]bool
	}
	got := map[string]*used{}
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		act, err := h.d.Nodes.Activity(ctx, n.ID)
		if err != nil {
			continue // a node that is off answers nothing; the rest still tell theirs
		}
		for _, c := range act.Clients {
			if !mine[c.Slot] {
				continue
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
				u := got[preset]
				if u == nil {
					u = &used{title: title, devices: map[string]bool{}, nodes: map[int64]bool{}}
					got[preset] = u
				}
				if ts > u.last {
					u.last = ts
				}
				u.devices[c.IP] = true
				u.nodes[n.ID] = true
			}
		}
	}
	for preset, u := range got {
		out.Body.Items = append(out.Body.Items, UserProtocolView{
			Preset: preset, Title: u.title, LastSeen: ptrUnix(u.last), Devices: len(u.devices), Nodes: len(u.nodes),
		})
	}
	sort.Slice(out.Body.Items, func(i, j int) bool {
		a, b := out.Body.Items[i], out.Body.Items[j]
		if a.LastSeen != nil && b.LastSeen != nil && !a.LastSeen.Equal(*b.LastSeen) {
			return a.LastSeen.After(*b.LastSeen)
		}
		return a.Preset < b.Preset
	})
	return out, nil
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
