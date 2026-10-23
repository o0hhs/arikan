package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// Outbounds the admin added by a share link: another server of their own that an inbound's
// traffic leaves through. The panel keeps the link and shows where it is used; the reading
// happens on the node, which carries mihomo's parser.

type OutboundUse struct {
	InboundID int64  `json:"inbound_id"`
	Inbound   string `json:"inbound"`
	NodeID    int64  `json:"node_id"`
	Node      string `json:"node" doc:"Нода, к которой подключён вход"`
}

type OutboundView struct {
	ID        int64         `json:"id"`
	Name      string        `json:"name"`
	Kind      string        `json:"kind" doc:"Протокол ссылки: vless, vmess, trojan, ss и другие"`
	Link      string        `json:"link" doc:"Ссылка, как она была вставлена"`
	Enabled   bool          `json:"enabled"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Uses      []OutboundUse `json:"uses" doc:"Входы, чей трафик выходит через эту исходящую"`
}

type outboundsOutput struct {
	Body struct {
		Outbounds []OutboundView `json:"outbounds"`
	}
}

type outboundOutput struct{ Body OutboundView }

type outboundIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

// OutboundCheck is what one node said about an outbound: the address websites see through
// it, where that address is, and why it did not work.
type OutboundCheck struct {
	OK      bool   `json:"ok"`
	IP      string `json:"ip,omitempty" doc:"Адрес, который видят сайты"`
	Country string `json:"country,omitempty" doc:"Страна этого адреса по геобазе"`
	Flag    string `json:"flag,omitempty"`
	ASN     int    `json:"asn,omitempty" doc:"Номер автономной системы адреса"`
	Org     string `json:"org,omitempty" doc:"Чья это сеть"`
	Error   string `json:"error,omitempty"`
	// Code: why not ok. not_applied — the node does not carry the outbound yet (it is
	// applied within half a minute, and a link that does not read never arrives);
	// unused — no inbound leaves through it; off — it is switched off; then the node's
	// own words for a timeout, a refusal and so on.
	Code      string    `json:"code,omitempty" enum:"not_applied,unused,off,timeout,refused,unreachable,error"`
	Node      int64     `json:"node_id,omitempty"`
	NodeName  string    `json:"node,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

type outboundCheckOutput struct{ Body OutboundCheck }

type createOutboundInput struct {
	Body struct {
		Link string `json:"link" maxLength:"4096" doc:"Ссылка vless://, vmess://, trojan://, ss://…"`
		Name string `json:"name,omitempty" maxLength:"64" doc:"Название; пусто — имя из ссылки"`
	}
}

type patchOutboundInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Name    *string `json:"name,omitempty" maxLength:"64"`
		Link    *string `json:"link,omitempty" maxLength:"4096"`
		Enabled *bool   `json:"enabled,omitempty"`
	}
}

func (h *handlers) registerOutbounds() {
	tags := []string{"outbounds"}
	huma.Register(h.api, huma.Operation{OperationID: "list-outbounds", Method: http.MethodGet, Path: "/api/v1/outbounds", Summary: "Исходящие, добавленные по ссылке", Tags: tags}, h.listOutbounds)
	huma.Register(h.api, huma.Operation{OperationID: "create-outbound", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/outbounds", Summary: "Добавить исходящую по ссылке", Tags: tags, DefaultStatus: http.StatusCreated}, h.createOutbound)
	huma.Register(h.api, huma.Operation{OperationID: "update-outbound", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPatch, Path: "/api/v1/outbounds/{id}", Summary: "Переименовать, заменить ссылку или выключить исходящую", Tags: tags}, h.updateOutbound)
	huma.Register(h.api, huma.Operation{OperationID: "delete-outbound", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodDelete, Path: "/api/v1/outbounds/{id}", Summary: "Удалить исходящую", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteOutbound)
	huma.Register(h.api, huma.Operation{OperationID: "check-outbound", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/outbounds/{id}/check", Summary: "Проверить исходящую: какой адрес видят сайты через неё", Tags: tags}, h.checkOutbound)
}

func (h *handlers) listOutbounds(ctx context.Context, _ *struct{}) (*outboundsOutput, error) {
	rows, err := h.d.Store.Q.ListOutbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := &outboundsOutput{}
	out.Body.Outbounds = []OutboundView{}
	labels, uses, err := h.outboundUses(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range rows {
		out.Body.Outbounds = append(out.Body.Outbounds, outboundView(o, uses[o.ID], labels))
	}
	return out, nil
}

// outboundUses lists what leaves through which outbound, with the node names of the
// inbounds, in one read for the whole list.
func (h *handlers) outboundUses(ctx context.Context) (map[int64]string, map[int64][]OutboundUse, error) {
	q := h.d.Store.Q
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	labels := map[int64]string{}
	for _, n := range nodes {
		switch {
		case n.Name != "":
			labels[n.ID] = n.Name
		case n.PublicHost != "":
			labels[n.ID] = n.PublicHost
		default:
			labels[n.ID] = "the panel's node"
		}
	}
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, nil, err
	}
	uses := map[int64][]OutboundUse{}
	for _, in := range ins {
		if !in.OutboundID.Valid {
			continue
		}
		uses[in.OutboundID.Int64] = append(uses[in.OutboundID.Int64], OutboundUse{InboundID: in.ID, Inbound: in.Name, NodeID: in.NodeID, Node: labels[in.NodeID]})
	}
	return labels, uses, nil
}

func outboundView(o db.Outbound, uses []OutboundUse, labels map[int64]string) OutboundView {
	v := OutboundView{ID: o.ID, Name: o.Name, Kind: linkKind(o.Link), Link: o.Link, Enabled: o.Enabled != 0,
		CreatedAt: time.Unix(o.CreatedAt, 0).UTC(), UpdatedAt: time.Unix(o.UpdatedAt, 0).UTC(), Uses: []OutboundUse{}}
	if uses != nil {
		v.Uses = uses
	}
	return v
}

// linkKind is the protocol of a link, as it is written before "://".
func linkKind(link string) string {
	for i := range link {
		if i+3 <= len(link) && link[i:i+3] == "://" {
			return link[:i]
		}
		if link[i] == '/' || link[i] == ':' {
			break
		}
	}
	return ""
}

// ownHosts are the addresses of this panel's own servers: a link that points at one of
// them would send an inbound's traffic back into the node it came from.
func (h *handlers) ownHosts(ctx context.Context) []string {
	var own []string
	for _, key := range []string{settings.KeyPublicHost, settings.KeyDomain, settings.KeySubDomain} {
		if v, err := h.d.Settings.String(ctx, key); err == nil && v != "" {
			own = append(own, v)
		}
	}
	if nodes, err := h.d.Store.Q.ListNodes(ctx); err == nil {
		for _, n := range nodes {
			own = append(own, n.PublicHost, n.Domain, n.Address)
		}
	}
	return own
}

// outboundError maps the domain's refusals to the API's codes. The refinements the web
// shows are the details' own codes (bad_link, own_server) with their value: the panel's
// other lists say what is wrong the same way.
func (h *handlers) outboundError(ctx context.Context, id int64, err error) error {
	var inUse *domain.NameInUseError
	switch {
	case errors.Is(err, domain.ErrOutboundLink):
		return fieldErr("body.link", "bad_link")
	case errors.Is(err, domain.ErrOutboundSelf):
		return fieldErr("body.link", "own_server")
	case errors.Is(err, domain.ErrOutboundName):
		return fieldErr("body.name", "bad_name")
	case errors.Is(err, domain.ErrOutboundFull):
		return huma.Error409Conflict("outbound_full", &huma.ErrorDetail{Location: "body.link", Message: "outbound_full", Value: domain.MaxOutbounds})
	case errors.Is(err, domain.ErrOutboundUsed):
		used := 0
		if list, err := domain.OutboundUsesOf(ctx, h.d.Store.Q, id); err == nil {
			used = len(list)
		}
		return huma.Error409Conflict("outbound_used", &huma.ErrorDetail{Location: "body.outbound_id", Message: "outbound_used", Value: used})
	case errors.As(err, &inUse):
		return huma.Error409Conflict("name_in_use", &huma.ErrorDetail{Location: "body.name", Message: "outbound_taken", Value: inUse.Owner})
	case errors.Is(err, sql.ErrNoRows):
		return huma.Error404NotFound("not_found")
	}
	return err
}

func (h *handlers) createOutbound(ctx context.Context, in *createOutboundInput) (*outboundOutput, error) {
	row, err := domain.AddOutbound(ctx, h.d.Store.Q, in.Body.Link, in.Body.Name, h.ownHosts(ctx), h.d.Now())
	if err != nil {
		return nil, h.outboundError(ctx, 0, err)
	}
	h.d.Changes.SlotsChanged()
	admin := sessionOf(ctx).AdminID
	h.audit(ctx, admin, "outbound.create", "outbound", strconv.FormatInt(row.ID, 10), map[string]any{"name": row.Name, "kind": linkKind(row.Link)})
	return &outboundOutput{Body: outboundView(row, nil, nil)}, nil
}

func (h *handlers) updateOutbound(ctx context.Context, in *patchOutboundInput) (*outboundOutput, error) {
	b := in.Body
	row, err := domain.UpdateOutbound(ctx, h.d.Store.Q, in.ID, domain.OutboundPatch{Name: b.Name, Link: b.Link, Enabled: b.Enabled}, h.ownHosts(ctx), h.d.Now())
	if err != nil {
		return nil, h.outboundError(ctx, in.ID, err)
	}
	h.d.Changes.SlotsChanged()
	admin := sessionOf(ctx).AdminID
	h.audit(ctx, admin, "outbound.update", "outbound", strconv.FormatInt(row.ID, 10),
		map[string]any{"name": row.Name, "link_changed": b.Link != nil, "enabled": row.Enabled != 0})
	return &outboundOutput{Body: outboundView(row, nil, nil)}, nil
}

func (h *handlers) deleteOutbound(ctx context.Context, in *outboundIDInput) (*struct{}, error) {
	if err := domain.DeleteOutbound(ctx, h.d.Store.Q, in.ID); err != nil {
		return nil, h.outboundError(ctx, in.ID, err)
	}
	h.d.Changes.SlotsChanged()
	admin := sessionOf(ctx).AdminID
	h.audit(ctx, admin, "outbound.delete", "outbound", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

// checkOutbound asks the node that carries it what websites see through the outbound. The
// nodes are tried in turn: several may carry it, and one of them being down says nothing
// about the outbound itself.
func (h *handlers) checkOutbound(ctx context.Context, in *outboundIDInput) (*outboundCheckOutput, error) {
	o, err := h.d.Store.Q.GetOutbound(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	out := &outboundCheckOutput{}
	out.Body.CheckedAt = h.d.Now().UTC()
	out.Body.NodeName = ""
	if o.Enabled == 0 {
		out.Body.Code = "off"
		return out, nil
	}
	if h.d.Nodes == nil {
		return nil, huma.Error409Conflict("nodes_disabled")
	}
	_, uses, err := h.outboundUses(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, u := range uses[o.ID] {
		if !slices.Contains(ids, u.NodeID) {
			ids = append(ids, u.NodeID)
		}
	}
	if len(ids) == 0 {
		out.Body.Code = "unused"
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out.Body.Node = ids[0]
	var lastErr error
	for _, id := range ids {
		p, err := h.d.Nodes.Probe(ctx, id, nodeapi.OutboundName(o.ID))
		if err == nil && p.OK {
			out.Body.OK, out.Body.IP = true, p.IP
			out.Body.Node = id
			h.geoCheck(&out.Body, p.IP)
			return out, nil
		}
		lastErr = err
		out.Body.Node = id
		if errors.Is(err, nodeapi.ErrUnavailable) {
			continue // this node is down: another may carry the outbound
		}
		if err != nil && !probeUnknown(err) {
			out.Body.Error = err.Error()
			out.Body.Code = nodeapi.Classify(err)
			return out, nil
		}
		// The node answered, and does not carry the outbound: it has not applied the state
		// with it yet, or the link does not read on it.
		out.Body.Code = "not_applied"
		return out, nil
	}
	out.Body.Code = "unreachable"
	if lastErr != nil {
		out.Body.Error = lastErr.Error()
	}
	return out, nil
}

// probeUnknown: the node answered that the proxy is not one of its own (404 no_such_outbound).
func probeUnknown(err error) bool {
	var se *nodeapi.StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

// geoCheck fills where the address is, when the panel knows.
func (h *handlers) geoCheck(out *OutboundCheck, ip string) {
	if h.d.Geo == nil || ip == "" {
		return
	}
	info := h.d.Geo.Addr(ip)
	out.Country, out.Flag = info.Country, info.Flag
	out.ASN, out.Org = info.ASN, info.Org
}
