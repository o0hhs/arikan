package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
)

// The disk of a node and its cleanup: what the node's data directory has free, what its
// directories take, and a one-button clean of the junk (update leftovers, temporary
// files). The heavy part of what fills a node's server — old release images — is the
// updater's, which prunes them after every successful update.

type DiskDirView struct {
	Path  string `json:"path" doc:"Относительно каталога данных ноды: mihomo, update, …"`
	Bytes int64  `json:"bytes"`
}

type DiskView struct {
	Total int64         `json:"total" doc:"Размер файловой системы каталога данных, байт"`
	Free  int64         `json:"free" doc:"Свободно, байт"`
	Used  int64         `json:"used" doc:"Занято, байт"`
	Dirs  []DiskDirView `json:"dirs"`
	Junk  int64         `json:"junk" doc:"Мусор, который уберёт очистка: остатки обновлений и временные файлы, байт"`
	Files []string      `json:"files" doc:"Эти файлы уберёт очистка"`
	Freed int64         `json:"freed" doc:"Сколько убрала последняя очистка, байт; 0 если её не было"`
}

type nodeDiskOutput struct{ Body DiskView }

func (h *handlers) registerNodeDisk() {
	tags := []string{"node"}
	huma.Register(h.api, huma.Operation{OperationID: "node-disk", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/disk",
		Summary: "Диск ноды", Description: "Файловая система каталога данных ноды и что в ней занимает место; мусор, который уберёт очистка.", Tags: tags}, h.nodeDisk)
	huma.Register(h.api, huma.Operation{OperationID: "clean-node-disk", Method: http.MethodPost, Path: "/api/v1/nodes/{id}/disk/clean",
		Summary: "Почистить диск ноды", Description: "Убирает остатки обновлений и временные файлы в каталоге данных ноды. Старые образы релизов чистит сам установщик после успешного обновления.",
		Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt, DefaultStatus: http.StatusOK}, h.cleanNodeDisk)
}

func diskView(s nodeapi.DiskStatus) DiskView {
	dirs := make([]DiskDirView, 0, len(s.Dirs))
	for _, d := range s.Dirs {
		dirs = append(dirs, DiskDirView{Path: d.Path, Bytes: d.Bytes})
	}
	return DiskView{Total: s.Total, Free: s.Free, Used: s.Used, Dirs: dirs, Junk: s.Junk, Files: s.Files, Freed: s.Freed}
}

func (h *handlers) nodeDisk(ctx context.Context, in *nodeIDInput) (*nodeDiskOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	if h.d.Nodes == nil {
		return nil, huma.Error502BadGateway("node_unavailable")
	}
	s, err := h.d.Nodes.Disk(ctx, in.ID)
	if err != nil {
		return nil, nodeDiskError(err, h.d.Log, in.ID)
	}
	return &nodeDiskOutput{Body: diskView(s)}, nil
}

func (h *handlers) cleanNodeDisk(ctx context.Context, in *nodeIDInput) (*nodeDiskOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	if h.d.Nodes == nil {
		return nil, huma.Error502BadGateway("node_unavailable")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "node.clean_disk", "node", strconv.FormatInt(in.ID, 10), nil)
	s, err := h.d.Nodes.CleanDisk(ctx, in.ID)
	if err != nil {
		return nil, nodeDiskError(err, h.d.Log, in.ID)
	}
	return &nodeDiskOutput{Body: diskView(s)}, nil
}

// nodeDiskError turns the node's refusals into the API's codes.
func nodeDiskError(err error, log *slog.Logger, node int64) error {
	var ne *nodeapi.Error
	var se *nodeapi.StatusError
	switch {
	case errors.As(err, &se) && se.Status == http.StatusNotFound:
		// A node older than the endpoint answers 404: it is simply too old to manage.
		return huma.Error409Conflict("node_too_old")
	case errors.As(err, &ne) && ne.Code == "disk_full":
		return huma.Error409Conflict("node_disk_full")
	}
	if log != nil {
		log.Warn("node disk", "node", node, "err", err)
	}
	if errors.Is(err, nodeapi.ErrUnavailable) {
		return huma.Error502BadGateway("node_unavailable")
	}
	return huma.Error502BadGateway("node_disk_failed")
}
