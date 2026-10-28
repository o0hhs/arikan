package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/s3backup"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Dangerous actions wait for a second admin when a custom admin asks for them. A
// superadmin keeps the old direct path; a custom admin's request is recorded, approved or
// rejected by another admin, and then performed once. The SQL refuses self-approval even
// if a handler is lost.

const approvalTTL = 24 * time.Hour

var approvalActionPermission = map[string]string{
	"user.delete":              PermUsersWrite,
	"user.reissue":             PermUsersWrite,
	"payment.refund":           PermBillingWrite,
	"payment.refunded_by_hand": PermBillingWrite,
	"s3.restore":               PermBackupsWrite,
}

type ApprovalView struct {
	ID          int64           `json:"id"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	Summary     string          `json:"summary"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status" enum:"pending,approved,executing,rejected,expired,used"`
	RequestedBy int64           `json:"requested_by"`
	RequestedAt time.Time       `json:"requested_at"`
	DecidedBy   *int64          `json:"decided_by,omitempty"`
	DecidedAt   *time.Time      `json:"decided_at,omitempty"`
	ExpiresAt   time.Time       `json:"expires_at"`
	UsedAt      *time.Time      `json:"used_at,omitempty"`
}

type approvalListInput struct {
	Status      string `query:"status" enum:"pending,approved,executing,rejected,expired,used," default:""`
	RequestedBy int64  `query:"requested_by" minimum:"0" default:"0"`
	Limit       int    `query:"limit" minimum:"1" maximum:"200" default:"100"`
}

type approvalCreateInput struct {
	Body struct {
		Action     string          `json:"action" enum:"user.delete,user.reissue,payment.refund,payment.refunded_by_hand,s3.restore"`
		TargetType string          `json:"target_type" minLength:"1" maxLength:"64"`
		TargetID   string          `json:"target_id" minLength:"1" maxLength:"256"`
		Summary    string          `json:"summary,omitempty" maxLength:"500"`
		Payload    json.RawMessage `json:"payload,omitempty"`
		ExpiresIn  int64           `json:"expires_in,omitempty" minimum:"60" maximum:"604800" doc:"Секунды; по умолчанию сутки"`
	}
}

type approvalIDInput struct {
	ID int64 `path:"id"`
}
type approvalListOutput struct{ Body []ApprovalView }
type approvalOutput struct{ Body ApprovalView }
type approvalExecuteOutput struct {
	Body struct {
		ApprovalID int64  `json:"approval_id"`
		Action     string `json:"action"`
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
		Status     string `json:"status" enum:"used"`
	}
}

func (h *handlers) registerApprovals() {
	tags := []string{"approvals"}
	huma.Register(h.api, huma.Operation{OperationID: "list-approvals", Method: http.MethodGet, Path: "/api/v1/approvals", Summary: "Запросы на опасные действия", Tags: tags}, h.listApprovals)
	huma.Register(h.api, huma.Operation{OperationID: "create-approval", Method: http.MethodPost, Path: "/api/v1/approvals", Summary: "Запросить подтверждение действия", Tags: tags, DefaultStatus: http.StatusCreated}, h.createApproval)
	huma.Register(h.api, huma.Operation{OperationID: "approve-approval", Method: http.MethodPost, Path: "/api/v1/approvals/{id}/approve", Summary: "Подтвердить действие", Tags: tags}, h.approveApproval)
	huma.Register(h.api, huma.Operation{OperationID: "reject-approval", Method: http.MethodPost, Path: "/api/v1/approvals/{id}/reject", Summary: "Отклонить действие", Tags: tags}, h.rejectApproval)
	huma.Register(h.api, huma.Operation{OperationID: "execute-approval", Method: http.MethodPost, Path: "/api/v1/approvals/{id}/execute", Summary: "Выполнить подтверждённое действие", Tags: tags}, h.executeApproval)
}

func approvalView(a db.ApprovalRequest) ApprovalView {
	v := ApprovalView{
		ID: a.ID, Action: a.Action, TargetType: a.TargetType, TargetID: a.TargetID, Summary: a.Summary,
		Payload: json.RawMessage(a.Payload), Status: a.Status, RequestedBy: a.RequestedBy,
		RequestedAt: store.Time(a.RequestedAt), ExpiresAt: store.Time(a.ExpiresAt),
	}
	if len(v.Payload) == 0 || !json.Valid(v.Payload) {
		v.Payload = json.RawMessage("{}")
	}
	if a.DecidedBy.Valid {
		x := a.DecidedBy.Int64
		v.DecidedBy = &x
	}
	if a.DecidedAt.Valid {
		x := store.Time(a.DecidedAt.Int64)
		v.DecidedAt = &x
	}
	if a.UsedAt.Valid {
		x := store.Time(a.UsedAt.Int64)
		v.UsedAt = &x
	}
	return v
}

func (h *handlers) listApprovals(ctx context.Context, in *approvalListInput) (*approvalListOutput, error) {
	role, perms := h.requestAccess(ctx)
	if !hasPermission(role, perms, PermApprovalsRead) && !hasPermission(role, perms, PermApprovalsDecide) {
		return nil, huma.Error403Forbidden("role_forbidden")
	}
	rows, err := h.d.Store.Q.ListApprovals(ctx, db.ListApprovalsParams{Status: in.Status, RequestedBy: in.RequestedBy, Lim: int32(in.Limit)})
	if err != nil {
		return nil, err
	}
	out := &approvalListOutput{Body: make([]ApprovalView, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, approvalView(r))
	}
	return out, nil
}

func (h *handlers) createApproval(ctx context.Context, in *approvalCreateInput) (*approvalOutput, error) {
	action := strings.TrimSpace(in.Body.Action)
	perm, ok := approvalActionPermission[action]
	if !ok {
		return nil, fieldErr("body.action", "bad_action")
	}
	role, perms := h.requestAccess(ctx)
	if !hasPermission(role, perms, perm) {
		return nil, huma.Error403Forbidden("role_forbidden")
	}
	targetType, targetID := strings.TrimSpace(in.Body.TargetType), strings.TrimSpace(in.Body.TargetID)
	if targetType == "" || targetID == "" {
		return nil, fieldErr("body.target_id", "bad_target")
	}
	payload := "{}"
	if len(in.Body.Payload) > 0 && string(in.Body.Payload) != "null" {
		if !json.Valid(in.Body.Payload) {
			return nil, fieldErr("body.payload", "bad_payload")
		}
		payload = string(in.Body.Payload)
	}
	ttl := approvalTTL
	if in.Body.ExpiresIn > 0 {
		ttl = time.Duration(in.Body.ExpiresIn) * time.Second
	}
	now := h.d.Now()
	a, err := h.d.Store.Q.CreateApproval(ctx, db.CreateApprovalParams{
		Action: action, TargetType: targetType, TargetID: targetID, Summary: strings.TrimSpace(in.Body.Summary),
		Payload: payload, RequestedBy: sessionOf(ctx).AdminID, RequestedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix(),
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "approval.request", "approval", strconv.FormatInt(a.ID, 10), map[string]any{"action": action, "target": targetType + ":" + targetID})
	return &approvalOutput{Body: approvalView(a)}, nil
}

func (h *handlers) approveApproval(ctx context.Context, in *approvalIDInput) (*approvalOutput, error) {
	return h.decideApproval(ctx, in.ID, "approved")
}

func (h *handlers) rejectApproval(ctx context.Context, in *approvalIDInput) (*approvalOutput, error) {
	return h.decideApproval(ctx, in.ID, "rejected")
}

func (h *handlers) decideApproval(ctx context.Context, id int64, status string) (*approvalOutput, error) {
	if err := h.requirePermission(ctx, PermApprovalsDecide); err != nil {
		return nil, err
	}
	a, err := h.d.Store.Q.GetApproval(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	actor := sessionOf(ctx).AdminID
	if a.RequestedBy == actor {
		return nil, huma.Error403Forbidden("self_approval")
	}
	if a.Status != "pending" {
		return nil, huma.Error409Conflict("approval_not_pending")
	}
	now := h.d.Now().Unix()
	n, err := h.d.Store.Q.DecideApproval(ctx, db.DecideApprovalParams{Status: status, DecidedBy: actor, DecidedAt: now, ID: id})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error409Conflict("approval_not_pending")
	}
	a, err = h.d.Store.Q.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	h.audit(ctx, actor, "approval."+status, "approval", strconv.FormatInt(id, 10), map[string]any{"action": a.Action, "requested_by": a.RequestedBy})
	return &approvalOutput{Body: approvalView(a)}, nil
}

func (h *handlers) executeApproval(ctx context.Context, in *approvalIDInput) (*approvalExecuteOutput, error) {
	a, err := h.d.Store.Q.GetApproval(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	actor := sessionOf(ctx).AdminID
	role, perms := h.requestAccess(ctx)
	if actor != a.RequestedBy && !hasPermission(role, perms, PermApprovalsDecide) {
		return nil, huma.Error403Forbidden("role_forbidden")
	}
	perm, ok := approvalActionPermission[a.Action]
	if !ok {
		return nil, huma.Error409Conflict("bad_action")
	}
	if !hasPermission(role, perms, perm) {
		return nil, huma.Error403Forbidden("role_forbidden")
	}
	if a.Status != "approved" {
		return nil, huma.Error409Conflict("approval_not_approved")
	}
	now := h.d.Now().Unix()
	if a.ExpiresAt <= now {
		return nil, huma.Error409Conflict("approval_expired")
	}
	n, err := h.d.Store.Q.ClaimApproval(ctx, db.ClaimApprovalParams{ExecutingAt: now, ID: a.ID})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error409Conflict("approval_not_approved")
	}
	if err := h.executeApprovalAction(ctx, a); err != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		n, rerr := h.d.Store.Q.RevertApproval(rctx, a.ID)
		cancel()
		if rerr != nil {
			h.d.Log.Error("approval revert", "approval", a.ID, "err", rerr)
		} else if n != 1 {
			h.d.Log.Warn("approval revert: approval is no longer used", "approval", a.ID)
		}
		h.audit(ctx, actor, "approval.use_failed", "approval", strconv.FormatInt(a.ID, 10), map[string]any{"action": a.Action, "error": err.Error()})
		return nil, err
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	n, err = h.d.Store.Q.FinishApproval(fctx, db.FinishApprovalParams{UsedAt: h.d.Now().Unix(), ID: a.ID})
	cancel()
	if err != nil {
		h.d.Log.Error("approval finish", "approval", a.ID, "err", err)
		return nil, huma.Error500InternalServerError("approval_finish_failed")
	}
	if n != 1 {
		h.d.Log.Warn("approval finish: approval is no longer executing", "approval", a.ID)
		return nil, huma.Error409Conflict("approval_not_executing")
	}
	h.audit(ctx, actor, "approval.use", "approval", strconv.FormatInt(a.ID, 10), map[string]any{"action": a.Action, "target": a.TargetType + ":" + a.TargetID})
	return &approvalExecuteOutput{Body: struct {
		ApprovalID int64  `json:"approval_id"`
		Action     string `json:"action"`
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
		Status     string `json:"status" enum:"used"`
	}{ApprovalID: a.ID, Action: a.Action, TargetType: a.TargetType, TargetID: a.TargetID, Status: "used"}}, nil
}

func (h *handlers) executeApprovalAction(ctx context.Context, a db.ApprovalRequest) error {
	id, err := strconv.ParseInt(a.TargetID, 10, 64)
	if err != nil {
		return huma.Error409Conflict("bad_target")
	}
	switch a.Action {
	case "user.delete":
		if err := h.d.Users.Delete(ctx, id); err != nil {
			return mapDomainErr(err)
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "user.delete", "user", a.TargetID, map[string]any{"approval": a.ID})
	case "user.reissue":
		if _, err := h.d.Users.Reissue(ctx, id); err != nil {
			return mapDomainErr(err)
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "user.reissue", "user", a.TargetID, map[string]any{"approval": a.ID})
	case "payment.refund":
		reverted, err := h.d.Billing.Refund(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return huma.Error404NotFound("not_found")
		}
		if errors.Is(err, billing.ErrNotRefunable) {
			return huma.Error409Conflict("not_refundable")
		}
		if err != nil {
			return huma.Error502BadGateway("refund_failed")
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "payment.refund", "payment", a.TargetID, map[string]any{"approval": a.ID, "reverted": reverted})
	case "payment.refunded_by_hand":
		if err := h.d.Billing.RefundedByHand(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return huma.Error404NotFound("not_found")
		} else if errors.Is(err, billing.ErrNotRefunable) {
			return huma.Error409Conflict("not_refundable")
		} else if err != nil {
			return err
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "payment.refunded_by_hand", "payment", a.TargetID, map[string]any{"approval": a.ID})
	case "s3.restore":
		if h.d.S3Backups == nil {
			return huma.Error503ServiceUnavailable("backup_unavailable")
		}
		var payload struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal([]byte(a.Payload), &payload)
		if strings.TrimSpace(payload.Key) == "" {
			return huma.Error409Conflict("s3_object_required")
		}
		if err := h.d.S3Backups.StartRestore(payload.Key); err != nil {
			if errors.Is(err, s3backup.ErrPending) {
				return huma.Error409Conflict("restore_pending")
			}
			return err
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "s3.backup.restore", "s3", payload.Key, map[string]any{"approval": a.ID})
	default:
		return huma.Error409Conflict("bad_action")
	}
	return nil
}

// requireApproval is called by a dangerous endpoint before it changes anything. A
// superadmin goes through; a custom admin gets a pending request and a 409 with its id.
func (h *handlers) requireApproval(ctx context.Context, action, targetType, targetID, summary string, payload any) error {
	role, perms := h.requestAccess(ctx)
	if role == RoleSuperadmin || role == "" {
		return nil
	}
	perm, ok := approvalActionPermission[action]
	if !ok || !hasPermission(role, perms, perm) {
		return huma.Error403Forbidden("role_forbidden")
	}
	now := h.d.Now()
	if a, err := h.d.Store.Q.GetPendingApproval(ctx, db.GetPendingApprovalParams{Action: action, TargetType: targetType, TargetID: targetID, RequestedBy: sessionOf(ctx).AdminID, ExpiresAt: now.Unix()}); err == nil {
		return approvalRequired(a.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	raw, _ := json.Marshal(payload)
	if string(raw) == "null" {
		raw = []byte("{}")
	}
	a, err := h.d.Store.Q.CreateApproval(ctx, db.CreateApprovalParams{
		Action: action, TargetType: targetType, TargetID: targetID, Summary: summary, Payload: string(raw),
		RequestedBy: sessionOf(ctx).AdminID, RequestedAt: now.Unix(), ExpiresAt: now.Add(approvalTTL).Unix(),
	})
	if err != nil {
		return err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "approval.request", "approval", strconv.FormatInt(a.ID, 10), map[string]any{"action": action, "target": targetType + ":" + targetID})
	return approvalRequired(a.ID)
}

func approvalRequired(id int64) error {
	return huma.Error409Conflict("approval_required", &huma.ErrorDetail{Location: "body", Message: "approval_required", Value: id})
}

// approvalSummary is a short, language-neutral line for the approvals page.
func approvalSummary(action, targetType, targetID string) string {
	return fmt.Sprintf("%s %s #%s", action, targetType, targetID)
}
