package api

import (
	"context"
	"time"

	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/subs"
)

// SubNodeView is one node that answers subscription links, as the subscription settings show
// it: when the panel last sent it the profiles, what it keeps, and whether it answers them.
type SubNodeView struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// LastPush is when the panel last sent the node the records; Error why it could not ("":
	// the last push went through). Records and Bytes are what the node kept of it.
	LastPush *time.Time `json:"last_push,omitempty" doc:"Когда панель последний раз отправила ноде подписки"`
	Error    string     `json:"error,omitempty" doc:"Почему последняя отправка не удалась"`
	Records  int        `json:"records" doc:"Сколько подписок нода хранит по последней отправке"`
	Bytes    int64      `json:"bytes,omitempty"`
	// Serving is the node's own report: it answers the links on its public 443, and why not
	// when it does not (subs_no_port — the port belongs to a protocol and no SNI router shares
	// it; subs_no_cert — nothing to present; subs_off — the feature is not in its state).
	Serving      bool   `json:"serving" doc:"Нода отвечает по ссылкам подписки"`
	ServingError string `json:"serving_error,omitempty" doc:"Почему не отвечает: subs_no_port, subs_no_cert или subs_off"`
}

// subNodes is how each chosen node is doing the job. A node the setting names but the table
// has not is left out: the setting's own check refuses new ones.
func (h *handlers) subNodes(ctx context.Context, cfg subs.FromNodes) ([]SubNodeView, error) {
	if !cfg.Enabled || len(cfg.Nodes) == 0 || h.d.Nodes == nil {
		return nil, nil
	}
	var pushes map[int64]nodesync.SubsPush
	if r, ok := h.d.Nodes.(interface {
		SubsStatus() map[int64]nodesync.SubsPush
	}); ok {
		pushes = r.SubsStatus()
	}
	out := make([]SubNodeView, 0, len(cfg.Nodes))
	for _, id := range cfg.Nodes {
		n, err := h.d.Store.Q.GetNode(ctx, id)
		if err != nil {
			continue
		}
		v := SubNodeView{ID: id, Name: n.Name, Enabled: n.Enabled != 0}
		if p, ok := pushes[id]; ok {
			v.Records, v.Bytes, v.Error = p.Records, p.Bytes, p.Err
			if !p.At.IsZero() {
				at := p.At
				v.LastPush = &at
			}
		}
		if hv, ok := h.d.Nodes.Health(id); ok && hv.OK && hv.Health.Subs != nil {
			// The node's own count is the truth about what it keeps: a push that failed
			// halfway left the rest of the previous one in place.
			v.Serving, v.ServingError = hv.Health.Subs.OK, hv.Health.Subs.Error
			v.Records, v.Bytes = hv.Health.Subs.Records, hv.Health.Subs.Bytes
		}
		out = append(out, v)
	}
	return out, nil
}
