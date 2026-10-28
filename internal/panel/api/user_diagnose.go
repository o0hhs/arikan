package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
)

// Why a subscriber cannot connect: the panel looks at the subscription itself, its term,
// traffic and devices, the protocols it may use and the nodes those protocols run on. The
// checks read; they change nothing.

type UserDiagnosisView struct {
	At      time.Time   `json:"at"`
	UserID  int64       `json:"user_id"`
	Summary string      `json:"summary" enum:"ok,warn,fail"`
	Items   []CheckItem `json:"items"`
}

type userDiagnosisOutput struct{ Body UserDiagnosisView }

func (h *handlers) registerUserDiagnosis() {
	huma.Register(h.api, huma.Operation{
		OperationID: "diagnose-user", Method: http.MethodGet, Path: "/api/v1/users/{id}/diagnose",
		Summary: "Почему у клиента не работает подписка", Tags: []string{"users"},
		Description: "Проверяет срок, трафик, устройства, тариф, протоколы, ноды и последнюю активность. Ничего не меняет.",
	}, h.diagnoseUser)
}

func (h *handlers) diagnoseUser(ctx context.Context, in *userIDInput) (*userDiagnosisOutput, error) {
	u, err := h.d.Users.Get(ctx, in.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	now := h.d.Now().UTC()
	items := make([]CheckItem, 0, 12)
	add := func(it CheckItem) { items = append(items, it) }

	grants, err := domain.UserGrantsLeft(ctx, h.d.Store.Q, u.ID, now)
	if err != nil {
		return nil, err
	}
	left := grants.Main(u.ID)
	state := domain.State(u, left, now)
	stateItem := CheckItem{ID: "user", Status: nodeapi.CheckOK, Params: map[string]string{"state": state}}
	switch state {
	case domain.StateDisabled:
		stateItem.Status, stateItem.Code, stateItem.Fix = nodeapi.CheckFail, "disabled", "enable_user"
	case domain.StateExpired:
		stateItem.Status, stateItem.Code, stateItem.Fix = nodeapi.CheckFail, "expired", "extend_user"
	case domain.StateLimited:
		stateItem.Status, stateItem.Code, stateItem.Fix = nodeapi.CheckFail, "traffic_exhausted", "reset_traffic"
	case domain.StateExpiring:
		stateItem.Status, stateItem.Code = nodeapi.CheckWarn, "expiring"
	}
	add(stateItem)

	// The link itself: a token and a slot are what the subscription page and the nodes use.
	link := CheckItem{ID: "subscription", Status: nodeapi.CheckOK, Params: map[string]string{"token": "present"}}
	if len(u.SubToken) != 24 {
		link.Status, link.Code, link.Fix = nodeapi.CheckFail, "no_token", "reissue_user"
	}
	if !u.SlotID.Valid {
		link.Status, link.Code, link.Fix = nodeapi.CheckFail, "no_slot", "reissue_user"
	}
	add(link)

	// The link as the client opens it: the panel itself asks its subscription endpoint. A
	// read-only key or a support operator without the subscribers' links does not get the
	// address, so the check is left out for them.
	if !hidesLinks(ctx) && len(u.SubToken) == 24 && u.SlotID.Valid {
		linkCheck := CheckItem{ID: "link", Status: nodeapi.CheckOK}
		base := ""
		if h.d.SubBase != nil {
			base = strings.TrimRight(h.d.SubBase(ctx), "/")
		}
		switch {
		case base == "":
			linkCheck.Status, linkCheck.Code = nodeapi.CheckFail, "no_link"
		case len(u.SubToken) != 24:
			linkCheck.Status, linkCheck.Code = nodeapi.CheckFail, "no_token"
		default:
			subURL := base + "/" + u.SubToken
			linkCheck.Params = map[string]string{"url": subURL}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, subURL, nil)
			if err != nil {
				linkCheck.Status, linkCheck.Code = nodeapi.CheckFail, "link_bad"
				break
			}
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				linkCheck.Status, linkCheck.Code = nodeapi.CheckFail, "link_unreachable"
				break
			}
			n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				linkCheck.Status, linkCheck.Code = nodeapi.CheckFail, "link_status"
				linkCheck.Params["status"] = strconv.Itoa(resp.StatusCode)
			} else if n == 0 {
				linkCheck.Status, linkCheck.Code = nodeapi.CheckFail, "link_empty"
			}
		}
		add(linkCheck)
	}

	expiry := CheckItem{ID: "expiry", Status: nodeapi.CheckOK}
	if u.ExpiresAt.Valid {
		days := (u.ExpiresAt.Int64 - now.Unix()) / 86400
		expiry.Params = map[string]string{"expires_at": time.Unix(u.ExpiresAt.Int64, 0).UTC().Format(time.RFC3339), "days_left": strconv.FormatInt(days, 10)}
		if u.ExpiresAt.Int64 <= now.Unix() {
			expiry.Status, expiry.Code, expiry.Fix = nodeapi.CheckFail, "expired", "extend_user"
		} else if days <= 3 {
			expiry.Status, expiry.Code = nodeapi.CheckWarn, "expiring"
		}
	} else {
		expiry.Params = map[string]string{"expires_at": "never"}
	}
	add(expiry)

	traffic := CheckItem{ID: "traffic", Status: nodeapi.CheckOK}
	if u.TrafficLimit.Valid {
		used := u.UsedUp + u.UsedDown
		traffic.Params = map[string]string{"limit": strconv.FormatInt(u.TrafficLimit.Int64, 10), "used": strconv.FormatInt(used, 10), "grants_left": strconv.FormatInt(left, 10)}
		if used >= u.TrafficLimit.Int64 && left <= 0 {
			traffic.Status, traffic.Code, traffic.Fix = nodeapi.CheckFail, "traffic_exhausted", "reset_traffic"
		} else if used >= u.TrafficLimit.Int64 {
			traffic.Status, traffic.Code = nodeapi.CheckWarn, "base_exhausted"
		}
	} else {
		traffic.Params = map[string]string{"limit": "unlimited"}
	}
	add(traffic)

	bound, err := h.d.Store.Q.CountBoundDevices(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	devices := CheckItem{ID: "devices", Status: nodeapi.CheckOK, Params: map[string]string{"bound": strconv.FormatInt(bound, 10), "mode": u.DeviceMode}}
	if u.DeviceLimit.Valid {
		devices.Params["limit"] = strconv.FormatInt(u.DeviceLimit.Int64, 10)
		if bound >= u.DeviceLimit.Int64 {
			devices.Status, devices.Code = nodeapi.CheckWarn, "device_limit"
		}
	}
	add(devices)

	closed := map[int64]bool{}
	if u.TariffID.Valid {
		t, terr := h.d.Store.Q.GetTariff(ctx, u.TariffID.Int64)
		if errors.Is(terr, sql.ErrNoRows) {
			add(CheckItem{ID: "tariff", Status: nodeapi.CheckFail, Code: "tariff_missing", Fix: "set_tariff"})
		} else if terr != nil {
			return nil, terr
		} else {
			for _, id := range domain.DecodeClosed(t.Closed) {
				closed[id] = true
			}
			it := CheckItem{ID: "tariff", Status: nodeapi.CheckOK, Params: map[string]string{"name": t.Name}}
			if t.Archived != 0 {
				it.Status, it.Code = nodeapi.CheckWarn, "tariff_archived"
			}
			add(it)
		}
	}

	allowed := map[int64]bool{}
	if u.Inbounds.Valid && u.Inbounds.String != "" {
		var ids []int64
		if json.Unmarshal([]byte(u.Inbounds.String), &ids) == nil {
			for _, id := range ids {
				allowed[id] = true
			}
		}
	}
	ins, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	usable := 0
	disabled := 0
	nodes := map[int64]bool{}
	for _, ib := range ins {
		if (len(allowed) > 0 && !allowed[ib.ID]) || closed[ib.ID] {
			continue
		}
		if ib.Enabled == 0 {
			disabled++
			continue
		}
		usable++
		nodes[ib.NodeID] = true
	}
	protocols := CheckItem{ID: "inbounds", Status: nodeapi.CheckOK, Params: map[string]string{"usable": strconv.Itoa(usable), "disabled": strconv.Itoa(disabled)}}
	switch {
	case usable == 0:
		protocols.Status, protocols.Code, protocols.Fix = nodeapi.CheckFail, "no_inbounds", "enable_inbound"
	case disabled > 0:
		protocols.Status, protocols.Code = nodeapi.CheckWarn, "some_disabled"
	}
	add(protocols)

	if h.d.Nodes != nil {
		for id := range nodes {
			hv, ok := h.d.Nodes.Health(id)
			it := CheckItem{ID: "node", Status: nodeapi.CheckOK, Params: map[string]string{"node_id": strconv.FormatInt(id, 10)}}
			if !ok {
				it.Status, it.Code = nodeapi.CheckWarn, "no_health"
			} else if !hv.OK {
				it.Status, it.Code = nodeapi.CheckFail, "node_down"
				if hv.Code != "" {
					it.Code = hv.Code
				}
			} else {
				bad := 0
				for _, l := range hv.Listeners {
					if !l.OK {
						bad++
					}
				}
				if bad > 0 {
					it.Status, it.Code = nodeapi.CheckWarn, "listener_down"
					it.Params["listeners_bad"] = strconv.Itoa(bad)
				}
			}
			add(it)
			if ok && hv.OK {
				for _, li := range listenerItems(hv) {
					if li.ID != "listener" {
						continue
					}
					if li.Params == nil {
						li.Params = map[string]string{}
					}
					li.Params["node_id"] = strconv.FormatInt(id, 10)
					add(li)
				}
			}
		}
	}

	activity := CheckItem{ID: "activity", Status: nodeapi.CheckOK}
	if u.OnlineAt.Valid {
		activity.Params = map[string]string{"online_at": time.Unix(u.OnlineAt.Int64, 0).UTC().Format(time.RFC3339)}
		if now.Unix()-u.OnlineAt.Int64 > 7*86400 {
			activity.Status, activity.Code = nodeapi.CheckWarn, "stale"
		}
	} else {
		activity.Status, activity.Code = nodeapi.CheckWarn, "never_online"
	}
	add(activity)

	summary := "ok"
	for _, it := range items {
		switch it.Status {
		case nodeapi.CheckFail:
			summary = "fail"
		case nodeapi.CheckWarn:
			if summary == "ok" {
				summary = "warn"
			}
		}
	}
	return &userDiagnosisOutput{Body: UserDiagnosisView{At: now, UserID: u.ID, Summary: summary, Items: items}}, nil
}
