package webui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// ActivityLimit is how many audit events the Activity page shows.
const ActivityLimit = 200

// addActivity is the Activity page: the audit log tail, newest first.
func addActivity(r *Registry) {
	r.AddPage(Page{Path: "/activity", Title: "Activity", Template: "activity.html", Load: loadActivity})
}

type activityRow struct {
	At     time.Time
	Type   string
	Who    string
	Item   string
	Detail string
}

func loadActivity(ctx context.Context, rq *Request) (any, error) {
	var r ipc.AuditReadResult
	if err := rq.Call(ctx, ipc.MethodAuditRead, ipc.AuditReadParams{Limit: ActivityLimit}, &r); err != nil {
		return nil, err
	}
	rows := make([]activityRow, 0, len(r.Events))
	for i := len(r.Events) - 1; i >= 0; i-- {
		e := r.Events[i]
		who := e.Alias
		if who == "" {
			who = shortID(string(e.Peer))
		}
		keys := make([]string, 0, len(e.Detail))
		for k := range e.Detail {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for j, k := range keys {
			parts[j] = fmt.Sprintf("%s=%v", k, e.Detail[k])
		}
		rows = append(rows, activityRow{
			At: e.TS, Type: cleanLine(e.Type), Who: cleanLine(who), Item: cleanLine(e.ItemID), Detail: cleanLine(strings.Join(parts, ", ")),
		})
	}
	return rows, nil
}
