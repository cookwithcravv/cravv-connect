package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerFiles(s *ipc.Server) {
	s.Register(ipc.MethodFileSend, ipc.Typed(h.fileSend), ipc.GateShared)
	s.Register(ipc.MethodFilesList, ipc.Typed(h.filesList), ipc.GateNone)
	s.Register(ipc.MethodFilesAccept, ipc.Typed(h.filesAccept), ipc.GateUnlock)
}

func (h *handlers) fileSend(ctx context.Context, cs *ipc.ConnState, p ipc.FileSendParams) (any, error) {
	if err := linkNumber(p.Link); err != nil {
		return nil, err
	}
	if err := required("path", p.Path); err != nil {
		return nil, err
	}
	ref, err := h.p.Files.Send(ctx, cs.Shared(), p.Link, cs.ProjectDir(), p.Path)
	if err != nil {
		return nil, err
	}
	return ipc.FileSendResult{FileID: ref.FileID}, nil
}

func (h *handlers) filesList(ctx context.Context, _ *ipc.ConnState, _ ipc.Empty) (any, error) {
	files, err := h.p.Files.List(ctx)
	if err != nil {
		return nil, err
	}
	out := ipc.FilesListResult{Files: make([]ipc.FileView, 0, len(files))}
	for _, f := range files {
		out.Files = append(out.Files, h.fileView(ctx, f))
	}
	return out, nil
}

func (h *handlers) filesAccept(ctx context.Context, cs *ipc.ConnState, p ipc.FileIDParams) (any, error) {
	if err := required("file_id", p.FileID); err != nil {
		return nil, err
	}
	return nil, h.p.Files.Accept(ctx, p.FileID, cs.Unlocked())
}
