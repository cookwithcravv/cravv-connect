package relayserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

// maxBufferedRoomMsgs bounds messages the creator may send before the joiner arrives.
const maxBufferedRoomMsgs = 16

var (
	errRoomBufferFull = errors.New("relayserver: room buffer full")
	errRoomBurned     = errors.New("relayserver: room already ended")
)

// liveRoom holds the connections of one pairing room while it is in use.
type liveRoom struct {
	nameplate string
	mu        sync.Mutex
	creator   *websocket.Conn
	joiner    *websocket.Conn
	buffered  []string // B64 data the creator sent before the joiner arrived
	timer     *time.Timer
	burned    bool // set under mu once the room has ended; no side may attach after
	once      sync.Once
}

// forward relays data to the other side, or buffers it until the joiner arrives.
func (lr *liveRoom) forward(ctx context.Context, side *websocket.Conn, data string) error {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	peer := lr.creator
	if side == lr.creator {
		peer = lr.joiner
	}
	if peer == nil {
		if len(lr.buffered) >= maxBufferedRoomMsgs {
			return errRoomBufferFull
		}
		lr.buffered = append(lr.buffered, data)
		return nil
	}
	return writeJSON(ctx, peer, relayproto.RoomMsg{T: relayproto.TypeMsg, Data: data})
}

// join attaches the joiner, sends it peer_joined and any buffered messages, then
// tells the creator. Holding mu keeps buffered messages ahead of live ones.
func (lr *liveRoom) join(ctx context.Context, ws *websocket.Conn) error {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.burned {
		return errRoomBurned
	}
	lr.joiner = ws
	joined := relayproto.RoomSignal{T: relayproto.TypePeerJoined}
	if err := writeJSON(ctx, ws, joined); err != nil {
		return err
	}
	for _, d := range lr.buffered {
		if err := writeJSON(ctx, ws, relayproto.RoomMsg{T: relayproto.TypeMsg, Data: d}); err != nil {
			return err
		}
	}
	lr.buffered = nil
	return writeJSON(ctx, lr.creator, joined)
}

// burn marks the room ended and returns the sides connected at that moment.
func (lr *liveRoom) burn() []*websocket.Conn {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.burned = true
	return []*websocket.Conn{lr.creator, lr.joiner}
}

// roomHub maps nameplates to live rooms.
type roomHub struct {
	mu    sync.Mutex
	rooms map[string]*liveRoom
}

func newRoomHub() *roomHub { return &roomHub{rooms: map[string]*liveRoom{}} }

// attachCreator creates the live room, returned with lr.mu held so that no joiner can
// announce itself before the creator has been sent "waiting". False if a creator is
// already connected.
func (h *roomHub) attachCreator(nameplate string, ws *websocket.Conn) (*liveRoom, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.rooms[nameplate]; ok {
		return nil, false
	}
	lr := &liveRoom{nameplate: nameplate, creator: ws}
	lr.mu.Lock()
	h.rooms[nameplate] = lr
	return lr, true
}

func (h *roomHub) get(nameplate string) *liveRoom {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rooms[nameplate]
}

func (h *roomHub) remove(nameplate string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.rooms, nameplate)
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	nameplate := strings.ToUpper(r.PathValue("nameplate"))
	token := r.URL.Query().Get(relayproto.QueryToken)
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(s.readLimit())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !s.ipLimiter.allow(clientIP(r)) {
		closeWithError(ctx, ws, relayproto.CodeRateLimited, "too many connections from this address")
		return
	}

	rec, err := s.be.GetRoom(ctx, nameplate)
	if errors.Is(err, ErrNotFound) {
		closeWithError(ctx, ws, relayproto.CodeNotFound, "no such pairing room")
		return
	}
	if err != nil {
		closeWithError(ctx, ws, relayproto.CodeInternal, "room store unavailable")
		return
	}
	now := s.cfg.Clock.Now()
	if !now.Before(rec.ExpiresAt) {
		_ = s.be.DeleteRoom(ctx, nameplate)
		closeWithError(ctx, ws, relayproto.CodeGone, "pairing room expired")
		return
	}
	var lr *liveRoom
	if token != "" {
		if !secretEqual(token, rec.CreatorToken) {
			closeWithError(ctx, ws, relayproto.CodeForbidden, "wrong creator token")
			return
		}
		var ok bool
		if lr, ok = s.rooms.attachCreator(nameplate, ws); !ok {
			closeWithError(ctx, ws, relayproto.CodeGone, "creator already connected")
			return
		}
		err := writeJSON(ctx, ws, relayproto.RoomSignal{T: relayproto.TypeWaiting})
		lr.timer = time.AfterFunc(rec.ExpiresAt.Sub(now), func() { s.expireRoom(lr) })
		lr.mu.Unlock()
		if err != nil {
			s.endRoom(ctx, lr, ws)
			return
		}
	} else {
		if lr = s.rooms.get(nameplate); lr == nil {
			closeWithError(ctx, ws, relayproto.CodeNotFound, "pairing room not open yet")
			return
		}
		claimed, err := s.be.ClaimJoin(ctx, nameplate)
		if err != nil || !claimed {
			closeWithError(ctx, ws, relayproto.CodeGone, "pairing room already used")
			return
		}
		err = lr.join(ctx, ws)
		if errors.Is(err, errRoomBurned) {
			closeWithError(ctx, ws, relayproto.CodeGone, "pairing room already ended")
			return
		}
		if err != nil {
			s.endRoom(ctx, lr, ws)
			return
		}
	}
	s.relayRoom(ctx, lr, ws, rec.ExpiresAt.Sub(now))
}

// relayRoom forwards msg frames from side until side closes or breaks the rules.
// Reads stop when the room's lifetime (ttl from now) ends, as a backstop to the
// room timer: nothing is read from a room past its ExpiresAt.
func (s *Server) relayRoom(ctx context.Context, lr *liveRoom, side *websocket.Conn, ttl time.Duration) {
	rctx, cancel := context.WithTimeout(ctx, ttl)
	defer cancel()
	fail := func(code, msg string) {
		s.endRoom(ctx, lr, side)
		closeWithError(ctx, side, code, msg)
	}
	for {
		h, raw, err := readFrame(rctx, side)
		if err != nil {
			if errors.Is(rctx.Err(), context.DeadlineExceeded) {
				s.expireRoom(lr)
			} else {
				s.endRoom(ctx, lr, side)
			}
			return
		}
		var m relayproto.RoomMsg
		if h.T != relayproto.TypeMsg || json.Unmarshal(raw, &m) != nil {
			fail(relayproto.CodeBadRequest, "expected msg")
			return
		}
		data, err := relayproto.UnB64(m.Data)
		if err != nil {
			fail(relayproto.CodeBadRequest, "data is not base64")
			return
		}
		if len(data) > s.cfg.Limits.MaxFrame {
			fail(relayproto.CodeTooLarge, "room message too large")
			return
		}
		if rec, err := s.be.GetRoom(ctx, lr.nameplate); err != nil || !s.cfg.Clock.Now().Before(rec.ExpiresAt) {
			s.expireRoom(lr)
			return
		}
		err = lr.forward(ctx, side, m.Data)
		if errors.Is(err, errRoomBufferFull) {
			fail(relayproto.CodeBadRequest, "too many messages before the peer joined")
			return
		}
		if err != nil {
			s.endRoom(ctx, lr, nil)
			return
		}
	}
}

// endRoom burns the room once and sends "closed" to every connected side except skip.
func (s *Server) endRoom(ctx context.Context, lr *liveRoom, skip *websocket.Conn) {
	s.burnRoom(ctx, lr, skip, relayproto.RoomSignal{T: relayproto.TypeClosed}, websocket.StatusNormalClosure)
}

// expireRoom burns the room once and sends error{gone} to every connected side.
func (s *Server) expireRoom(lr *liveRoom) {
	gone := relayproto.Error{T: relayproto.TypeError, Code: relayproto.CodeGone, Message: "pairing room expired"}
	s.burnRoom(context.Background(), lr, nil, gone, websocket.StatusPolicyViolation)
}

func (s *Server) burnRoom(ctx context.Context, lr *liveRoom, skip *websocket.Conn, notice any, code websocket.StatusCode) {
	lr.once.Do(func() {
		lr.mu.Lock()
		timer := lr.timer
		lr.mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		s.rooms.remove(lr.nameplate)
		_ = s.be.DeleteRoom(ctx, lr.nameplate)
		for _, ws := range lr.burn() {
			if ws == nil || ws == skip {
				continue
			}
			_ = writeJSON(ctx, ws, notice)
			go ws.Close(code, "room ended")
		}
	})
}
