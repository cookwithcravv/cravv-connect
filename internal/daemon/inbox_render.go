package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Rendered is the display form of one inbox item body.
type Rendered struct {
	ViewKind string // chat | task | task_update | file
	Text     string // unescaped; present.Wrap escapes it
	FileID   string
	Path     string
}

// Renderer turns a stored inbox item into display text.
type Renderer func(it store.InboxItem) Rendered

// RendererRegistry maps item kinds to renderers (new kinds register here).
type RendererRegistry struct {
	mu sync.RWMutex
	m  map[core.Kind]Renderer
}

// NewRendererRegistry returns an empty registry.
func NewRendererRegistry() *RendererRegistry {
	return &RendererRegistry{m: map[core.Kind]Renderer{}}
}

// Register adds or replaces the renderer for a kind.
func (r *RendererRegistry) Register(k core.Kind, fn Renderer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[k] = fn
}

// Render uses the kind's renderer, or shows the raw body for unknown kinds.
func (r *RendererRegistry) Render(it store.InboxItem) Rendered {
	r.mu.RLock()
	fn, ok := r.m[it.Kind]
	r.mu.RUnlock()
	if !ok {
		return Rendered{ViewKind: string(it.Kind), Text: string(it.Body)}
	}
	return fn(it)
}

// FileNotice is the inbox body for file items (kind core.KindFileOffer).
type FileNotice struct {
	FileID string          `json:"file_id"`
	Name   string          `json:"name"`
	Size   int64           `json:"size"`
	State  store.FileState `json:"state"`
	Path   string          `json:"path,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

// DefaultRenderers registers chat, task, task_update and file renderers.
func DefaultRenderers() *RendererRegistry {
	r := NewRendererRegistry()
	r.Register(core.KindChat, renderChat)
	r.Register(core.KindTaskCreate, renderTaskCreate)
	r.Register(core.KindTaskUpdate, renderTaskUpdate)
	r.Register(core.KindFileOffer, renderFileNotice)
	for _, k := range []core.Kind{core.KindLinkRequest, core.KindLinkAccepted, core.KindLinkRejected, core.KindLinkClosed} {
		r.Register(k, renderLinkNotice)
	}
	return r
}

// renderLinkNotice shows a link event. Humans decide requests with the CLI
// (Phase 2 adds the chat decision path).
func renderLinkNotice(it store.InboxItem) Rendered {
	n, err := decodeEnvBody[LinkNotice](it.Body)
	if err != nil {
		return Rendered{ViewKind: "link", Text: "(unreadable link notice)"}
	}
	var sb strings.Builder
	switch n.Event {
	case "request":
		fmt.Fprintf(&sb, "asks to link with this session as link %d, with permission %s.", n.Link, n.Permission)
		if n.Note != "" {
			fmt.Fprintf(&sb, "\nnote: %s", n.Note)
		}
		fmt.Fprintf(&sb, "\nA human decides with: cravv-connect link accept %d (or cravv-connect link reject %d)", n.Link, n.Link)
	case "accepted":
		fmt.Fprintf(&sb, "accepted link %d. On their session you may: %s.", n.Link, n.Permission)
	case "rejected":
		fmt.Fprintf(&sb, "link %d was not accepted: %s.", n.Link, n.Reason)
	default:
		fmt.Fprintf(&sb, "link %d closed: %s.", n.Link, n.Reason)
	}
	return Rendered{ViewKind: "link", Text: sb.String()}
}

func renderChat(it store.InboxItem) Rendered {
	b, err := decodeEnvBody[core.ChatBody](it.Body)
	if err != nil {
		return Rendered{ViewKind: "chat", Text: "(unreadable chat message)"}
	}
	return Rendered{ViewKind: "chat", Text: b.Text}
}

func renderTaskCreate(it store.InboxItem) Rendered {
	b, err := decodeEnvBody[core.TaskCreateBody](it.Body)
	if err != nil {
		return Rendered{ViewKind: "task", Text: "(unreadable task)"}
	}
	var sb strings.Builder
	sb.WriteString(b.Instructions)
	writeFileRefs(&sb, "attached files", b.Files)
	return Rendered{ViewKind: "task", Text: sb.String()}
}

func renderTaskUpdate(it store.InboxItem) Rendered {
	b, err := decodeEnvBody[core.TaskUpdateBody](it.Body)
	if err != nil {
		return Rendered{ViewKind: "task_update", Text: "(unreadable task update)"}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "state: %s", b.State)
	if b.Note != "" {
		fmt.Fprintf(&sb, "\nnote: %s", b.Note)
	}
	if b.Result != "" {
		fmt.Fprintf(&sb, "\nresult:\n%s", b.Result)
	}
	writeFileRefs(&sb, "result files", b.Files)
	return Rendered{ViewKind: "task_update", Text: sb.String()}
}

func renderFileNotice(it store.InboxItem) Rendered {
	n, err := decodeEnvBody[FileNotice](it.Body)
	if err != nil {
		return Rendered{ViewKind: "file", Text: "(unreadable file notice)"}
	}
	var text string
	switch n.State {
	case store.FileDone:
		text = fmt.Sprintf("file %q (%d bytes) saved to %s", n.Name, n.Size, n.Path)
	case store.FileHeld:
		text = fmt.Sprintf("file %q (%d bytes) is held until a human runs: cravv-connect files accept %s", n.Name, n.Size, n.FileID)
	default:
		text = fmt.Sprintf("file %q (%d bytes) %s: %s", n.Name, n.Size, n.State, n.Reason)
	}
	return Rendered{ViewKind: "file", Text: text, FileID: n.FileID, Path: n.Path}
}

func writeFileRefs(sb *strings.Builder, label string, refs []core.FileRef) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintf(sb, "\n%s:", label)
	for _, f := range refs {
		fmt.Fprintf(sb, "\n- %s (%d bytes, file_id %s)", f.Name, f.Size, f.FileID)
	}
}

func decodeEnvBody[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("decode body: %w", err)
	}
	return v, nil
}
