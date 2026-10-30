package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/store/db"
	"mikan/internal/textutil"
)

type CascadeHop struct {
	NodeID int64  `json:"node_id"`
	Name   string `json:"name"`
}

type ProbeView struct {
	OK        bool      `json:"ok"`
	IP        string    `json:"ip,omitempty" doc:"Адрес, который видят сайты"`
	Colo      string    `json:"colo,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// CascadeWarp is a node's WARP as a cascade card shows it: whether the panel has an
// account for the node, whether it is on, and the node's own last look through it.
type CascadeWarp struct {
	Configured bool       `json:"configured" doc:"У ноды есть аккаунт WARP"`
	Enabled    bool       `json:"enabled" doc:"WARP включён: трафик уходит через него, а не с адреса сервера"`
	Status     *WarpCheck `json:"status,omitempty" doc:"Последняя проверка выхода через WARP с этой ноды"`
}

type CascadeExit struct {
	CascadeHop
	Inbounds []string   `json:"inbounds" doc:"Подключения этой ноды, которые выходят через ту ноду"`
	Relay    bool       `json:"relay" doc:"Через неё идёт и трафик, который другие ноды передают через эту"`
	Probe    *ProbeView `json:"probe,omitempty" doc:"Проверка с этой ноды: какой IP видят сайты через цепочку"`
	// Tunnel is the exit's side of the panel's tunnel; nil on a node that cannot be an exit
	// (the panel's own node, or one without a join key).
	Tunnel *ExitTunnel `json:"tunnel,omitempty" doc:"Поток выхода к панели: в туннеле или нет"`
	// Chain is what this exit does with the traffic next: the nodes it sends on through, in
	// order. Empty when the traffic leaves the internet on this node itself.
	Chain []CascadeHop `json:"chain" doc:"Ноды после этой, по порядку: куда она передаёт трафик дальше"`
	// End is where the chain leaves the internet: the last node's own address or its WARP.
	End string `json:"end" enum:"direct,warp" doc:"Чем цепочка кончается: прямым выходом или WARP"`
	// Warp is the WARP the chain ends in; nil when End is direct.
	Warp *CascadeWarp `json:"warp,omitempty" doc:"WARP ноды, на которой цепочка кончается"`
}

// ExitTunnel is an exit's stream to the panel as it is now.
type ExitTunnel struct {
	Connected bool `json:"connected" doc:"Панель держит поток этой ноды: её трафик идёт через туннель"`
	// Reserve: the exit is not one any inbound named, only a reserve of another's tunnel.
	Reserve bool       `json:"reserve" doc:"Выход в резерве: подхватит, если у основного пропадёт туннель"`
	Since   *time.Time `json:"since,omitempty" doc:"С какого момента поток есть"`
}

// CascadeTunnel is this node's own part in the cascade over the panel's tunnel.
type CascadeTunnel struct {
	Entry bool `json:"entry" doc:"Нода отправляет трафик своих подключений через туннель панели"`
	Exit  bool `json:"exit" doc:"Через эту ноду выпускают трафик другие ноды (через туннель)"`
	// Bridge: the panel holds the node's bridge now (an entry's traffic can flow); Online:
	// the panel has the node's stream now (an exit can carry).
	Bridge      bool       `json:"bridge"`
	BridgeSince *time.Time `json:"bridge_since,omitempty"`
	Online      bool       `json:"online"`
	OnlineSince *time.Time `json:"online_since,omitempty"`
}

// The states of a relay's listener on the node (CascadeRelay.Listener).
const (
	relayUp      = "ok"
	relayBusy    = "busy"    // another program holds the port
	relayFailed  = "failed"  // it could not start, for another reason
	relayUnknown = "unknown" // the node does not answer, or has not applied the relay yet
)

// Where a chain leaves the internet: a node's own address or its WARP.
const (
	endDirect = "direct"
	endWarp   = "warp"
)

type CascadeRelay struct {
	Port       string       `json:"port"`
	Sources    []CascadeHop `json:"sources" doc:"Ноды, которые выходят через эту"`
	Outbound   string       `json:"outbound" enum:"direct,warp,node" doc:"Куда эта нода выпускает их трафик"`
	ExitNodeID *int64       `json:"exit_node_id,omitempty"`
	Listener   string       `json:"listener" enum:"ok,busy,failed,unknown" doc:"Слушает ли нода порт служебного входа: busy, если порт занят другой программой"`
	Error      string       `json:"error,omitempty" doc:"Почему служебный вход не запустился, как сказала нода"`
	// Chain is the same walk CascadeExit does, from this node: what the traffic other nodes
	// send here really goes on through, saved rows included.
	Chain []CascadeHop `json:"chain" doc:"Ноды после этой, по порядку: куда она передаёт трафик других нод"`
	End   string       `json:"end" enum:"direct,warp" doc:"Чем цепочка кончается: прямым выходом или WARP"`
	Warp  *CascadeWarp `json:"warp,omitempty" doc:"WARP ноды, на которой цепочка кончается"`
}

type CascadeView struct {
	Relay *CascadeRelay `json:"relay,omitempty" doc:"Служебный вход для других нод; есть, когда кто-то выходит через эту ноду"`
	Exits []CascadeExit `json:"exits" doc:"Ноды, через которые эта нода выпускает трафик"`
	// Tunnel is the cascade over the panel's tunnel: the entry's bridge or the exit's
	// stream. No exit is dialled directly any more.
	Tunnel *CascadeTunnel `json:"tunnel,omitempty" doc:"Каскад через туннель панели: мост входа или поток выхода"`
	// Warp is this node's own WARP: whether the panel has an account for it and whether it
	// is on. The route that sends other nodes' traffic there is saved only when it is.
	Warp CascadeWarp `json:"warp" doc:"WARP этой ноды: настроен ли и включён"`
}

type cascadeOutput struct{ Body CascadeView }

type cascadePatchInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Outbound   string `json:"outbound" enum:"direct,warp,node" doc:"Куда выпускать трафик других нод, пришедший через эту"`
		ExitNodeID int64  `json:"exit_node_id,omitempty" minimum:"0"`
	}
}

func (h *handlers) registerCascade() {
	tags := []string{"node"}
	huma.Register(h.api, huma.Operation{OperationID: "get-node-cascade", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/cascade", Summary: "Каскад ноды: выходы и служебный вход", Tags: tags}, h.getCascade)
	huma.Register(h.api, huma.Operation{OperationID: "update-node-cascade", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPatch, Path: "/api/v1/nodes/{id}/cascade", Summary: "Куда нода выпускает трафик других нод", Tags: tags}, h.patchCascade)
}

// cascadeRefusals are the domain's refusals of an exit the API reports by their own code.
var cascadeRefusals = []error{domain.ErrExitSelf, domain.ErrExitCycle, domain.ErrExitLong, domain.ErrExitOff, domain.ErrNoPort}

// cascadeError maps the domain's refusals to the API's codes.
func cascadeError(err error, field string) error {
	for _, e := range cascadeRefusals {
		if errors.Is(err, e) {
			return huma.Error422UnprocessableEntity("cascade", &huma.ErrorDetail{Location: "body." + field, Message: e.Error()})
		}
	}
	if errors.Is(err, domain.ErrNotFound) {
		return huma.Error422UnprocessableEntity("cascade", &huma.ErrorDetail{Location: "body." + field, Message: "exit_not_found"})
	}
	return err
}

// relayListener is the state of the relay's listener in a health check, and the node's
// words when it failed. A node that runs another port than the row's has not caught up
// with a move yet: what it says is not about this port.
func relayListener(hv nodesync.HealthView, port string) (state, why string) {
	if !hv.OK || hv.Ports[nodeapi.RelayListener] != port {
		return relayUnknown, ""
	}
	for _, l := range hv.Listeners {
		switch {
		case l.Name != nodeapi.RelayListener:
		case l.OK:
			return relayUp, ""
		case l.Busy():
			return relayBusy, l.Error
		default:
			return relayFailed, l.Error
		}
	}
	return relayUnknown, ""
}

// hostOf is what a node last said listens on its server; nil when nobody knows.
func (h *handlers) hostOf(id int64) *nodeapi.HostPorts {
	if h.d.Nodes == nil {
		return nil
	}
	if hv, ok := h.d.Nodes.Health(id); ok {
		return hv.HostPorts()
	}
	return nil
}

// exitChain follows the relay rows from node id: the nodes its traffic goes on through, in
// order, and where it leaves the internet. The rows are what the nodes' states are built
// from, so the card says what the traffic does rather than what the admin last chose. A
// chain the domain would not save (a loop, a hand-edited table) stops at the repeat.
func exitChain(id int64, relays map[int64]db.NodeRelay, names map[int64]string) ([]CascadeHop, string) {
	chain := []CascadeHop{}
	seen := map[int64]bool{id: true}
	cur := id
	for range 8 { // domain.maxChain: longer chains are not saved
		r, ok := relays[cur]
		if !ok {
			return chain, endDirect
		}
		if !r.ExitNodeID.Valid {
			if r.Outbound == "warp" {
				return chain, endWarp
			}
			return chain, endDirect
		}
		cur = r.ExitNodeID.Int64
		if seen[cur] {
			return chain, endDirect
		}
		seen[cur] = true
		chain = append(chain, CascadeHop{NodeID: cur, Name: names[cur]})
	}
	return chain, endDirect
}

// chainEnd is the node a chain ends on: its last hop, or the node the chain starts from.
func chainEnd(start int64, chain []CascadeHop) int64 {
	if len(chain) == 0 {
		return start
	}
	return chain[len(chain)-1].NodeID
}

// cascadeWarpOf is the WARP of the node a chain ends on, as the panel and the node itself
// know it. withStatus asks the node for its own look, and only while WARP is on: a node
// without it has nothing to look through, and a reserve is no path that carries now.
func (h *handlers) cascadeWarpOf(ctx context.Context, id int64, withStatus bool) (*CascadeWarp, error) {
	w := &CascadeWarp{}
	r, err := h.d.Store.Q.GetNodeWarp(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	w.Configured, w.Enabled = true, r.Enabled != 0
	if withStatus && w.Enabled && h.d.Nodes != nil {
		switch s, err := h.d.Nodes.Warp(ctx, id, false); {
		case err != nil:
			w.Status = &WarpCheck{Error: "node_unreachable", Detail: textutil.Clip(err.Error(), 200), CheckedAt: h.d.Now().UTC()}
		case s.Configured:
			w.Status = &WarpCheck{s.OK, s.IP, s.Warp, s.Colo, s.Error, s.Detail, s.CheckedAt}
		}
	}
	return w, nil
}

func (h *handlers) getCascade(ctx context.Context, in *nodeIDInput) (*cascadeOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	q := h.d.Store.Q
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	var self db.Node
	for _, n := range nodes {
		names[n.ID] = n.Name
		if n.ID == in.ID {
			self = n
		}
	}
	out := &cascadeOutput{Body: CascadeView{Exits: []CascadeExit{}}}
	switch w, err := q.GetNodeWarp(ctx, in.ID); {
	case err == nil:
		out.Body.Warp.Configured, out.Body.Warp.Enabled = true, w.Enabled != 0
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	relays, err := q.ListNodeRelays(ctx)
	if err != nil {
		return nil, err
	}
	// The relay rows are the same ones the nodes' states are built from: the chains below
	// are what the traffic really does, not what the card last said.
	rows := make(map[int64]db.NodeRelay, len(relays))
	for _, row := range relays {
		rows[row.NodeID] = row
	}
	r, err := q.GetNodeRelay(ctx, in.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		users, err := q.ListRelayUsers(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		v := &CascadeRelay{Port: r.Port, Sources: []CascadeHop{}, Outbound: r.Outbound, Listener: relayUnknown}
		out.Body.Relay = v
		for _, u := range users {
			v.Sources = append(v.Sources, CascadeHop{NodeID: u.SrcNodeID, Name: names[u.SrcNodeID]})
		}
		if r.ExitNodeID.Valid {
			id := r.ExitNodeID.Int64
			v.Outbound, v.ExitNodeID = "node", &id
		}
		v.Chain, v.End = exitChain(in.ID, rows, names)
		if v.End == endWarp {
			if v.Warp, err = h.cascadeWarpOf(ctx, chainEnd(in.ID, v.Chain), true); err != nil {
				return nil, err
			}
		}
		if h.d.Nodes != nil {
			if hv, ok := h.d.Nodes.Health(in.ID); ok {
				v.Listener, v.Error = relayListener(hv, r.Port)
			}
		}
	}
	idx := map[int64]int{}
	exit := func(id int64) *CascadeExit {
		if i, ok := idx[id]; ok {
			return &out.Body.Exits[i]
		}
		idx[id] = len(out.Body.Exits)
		out.Body.Exits = append(out.Body.Exits, CascadeExit{CascadeHop: CascadeHop{NodeID: id, Name: names[id]}, Inbounds: []string{}})
		return &out.Body.Exits[len(out.Body.Exits)-1]
	}
	for _, i := range ins {
		if i.NodeID == in.ID && i.ExitNodeID.Valid {
			e := exit(i.ExitNodeID.Int64)
			e.Inbounds = append(e.Inbounds, i.Name)
		}
	}
	if r.ExitNodeID.Valid {
		exit(r.ExitNodeID.Int64).Relay = true
	}
	// The cascade over the panel's tunnel: this node's part, and where each exit stands.
	plan := domain.CascadePlans(nodes, ins, relays)[in.ID]
	primary := map[int64]bool{}
	entry := false
	for _, r := range plan.Routes {
		primary[r.Exit] = true
		if !r.Tunnel {
			continue
		}
		entry = true
		for _, id := range r.Reserves {
			exit(id) // a reserve is shown too: it carries traffic when the main exit cannot
		}
	}
	// The panel's own node, and a node without a join key, cannot be an exit: no stream of
	// theirs is ever taken, and the card does not claim the part.
	canExit := plan.IsExit && self.Address != "" && self.CertSha256 != ""
	if entry || canExit {
		tv := &CascadeTunnel{Entry: entry, Exit: canExit}
		if h.d.Cascade != nil {
			if since, ok := h.d.Cascade.BridgeOnline(in.ID); ok {
				tv.Bridge, tv.BridgeSince = true, &since
			}
			if since, ok := h.d.Cascade.ExitOnline(in.ID); ok {
				tv.Online, tv.OnlineSince = true, &since
			}
		}
		out.Body.Tunnel = tv
	}
	for i := range out.Body.Exits {
		e := &out.Body.Exits[i]
		// A node that cannot be an exit (no join key) has no stream either, and the state
		// names it with a way to nowhere: its inbounds fail rather than leave directly.
		es := &ExitTunnel{Reserve: !primary[e.NodeID]}
		if h.d.Cascade != nil {
			if since, ok := h.d.Cascade.ExitOnline(e.NodeID); ok {
				es.Connected, es.Since = true, &since
			}
		}
		e.Tunnel = es
		e.Chain, e.End = exitChain(e.NodeID, rows, names)
		if e.End == endWarp {
			if e.Warp, err = h.cascadeWarpOf(ctx, chainEnd(e.NodeID, e.Chain), !es.Reserve); err != nil {
				return nil, err
			}
		}
		if h.d.Nodes == nil || es.Reserve {
			// A reserve is no proxy in the node's config (the proxy is named after the exit
			// the inbound chose), so there is nothing to probe through.
			continue
		}
		if p, err := h.d.Nodes.Probe(ctx, in.ID, nodeapi.ExitName(e.NodeID)); err == nil {
			e.Probe = &ProbeView{OK: p.OK, IP: p.IP, Colo: p.Colo, Error: p.Error, CheckedAt: p.CheckedAt}
		}
	}
	return out, nil
}

// warpRouteRefusal refuses to send other nodes' traffic to WARP a node does not have (or
// keeps off): the state would carry no WARP, and that traffic would leave from the exit's
// own address — the very leak the route was chosen to avoid. The state builder fails such a
// route closed for rows that get there another way (a restore, a hand edit); this keeps the
// admin from saving one.
func (h *handlers) warpRouteRefusal(ctx context.Context, nodeID int64) error {
	w, err := h.d.Store.Q.GetNodeWarp(ctx, nodeID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fieldErr("body.outbound", "warp_not_configured")
	case err != nil:
		return err
	case w.Enabled == 0:
		return fieldErr("body.outbound", "warp_off")
	}
	return nil
}

func (h *handlers) patchCascade(ctx context.Context, in *cascadePatchInput) (*cascadeOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	b := in.Body
	if b.Outbound == "warp" {
		if err := h.warpRouteRefusal(ctx, in.ID); err != nil {
			return nil, err
		}
	}
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		// The relay is made even before anyone uses the node as an exit: the route is
		// ready when they do.
		n, err := q.GetNode(ctx, in.ID)
		if err != nil {
			return err
		}
		if _, err := domain.EnsureRelay(ctx, q, n, h.d.Now(), h.hostOf(n.ID)); err != nil {
			return err
		}
		outbound, exit := b.Outbound, sql.NullInt64{}
		if outbound == "node" {
			if b.ExitNodeID == 0 {
				return domain.ErrNotFound
			}
			if err := domain.UseExit(ctx, q, in.ID, b.ExitNodeID, h.d.Now(), h.hostOf); err != nil {
				return err
			}
			outbound, exit = "direct", sql.NullInt64{Int64: b.ExitNodeID, Valid: true}
		}
		return q.SetNodeRelayRoute(ctx, db.SetNodeRelayRouteParams{Outbound: outbound, ExitNodeID: exit, NodeID: in.ID})
	})
	if err != nil {
		return nil, cascadeError(err, "exit_node_id")
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.cascade", "node", strconv.FormatInt(in.ID, 10), map[string]any{"outbound": b.Outbound, "exit_node_id": b.ExitNodeID})
	return h.getCascade(ctx, &nodeIDInput{ID: in.ID})
}
