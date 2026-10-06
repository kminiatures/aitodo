package mcp

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWatchInterval = 1500 * time.Millisecond
	// 接続直後は Claude Code 側の channel の受け口がまだ無いことがある（起動時の確認ダイアログ等）。
	// その間に知らせると捨てられて二度と届かないので、最初の確認を少し遅らせる
	defaultWatchDelay = 3 * time.Second
)

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// watchGo はサーバーの cwd から解決したセッションを定期的に見て、go になったタスクを
// notifications/claude/channel で Claude Code に知らせる。
//
// ステータスは変えない（知らせるだけ）。サーバーからは、Claude Code がこのサーバーを channel として
// 読み込んだかどうかが分からず、読み込んでいなければ通知は黙って捨てられるため。着手は Claude が
// task_claim で行い、同じフォルダで複数の Claude が動いていても取れるのは 1 つだけになる。
func (s *server) watchGo() {
	select {
	case <-s.stop:
		return
	case <-time.After(s.watchDelay):
	}
	notified := map[int64]bool{}
	tick := time.NewTicker(s.watchInterval)
	defer tick.Stop()
	for {
		s.pollGo(notified)
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
	}
}

func (s *server) pollGo(notified map[int64]bool) {
	sess, err := s.resolve(&args{})
	if err != nil { // このフォルダにセッションが無いのは普通のこと
		return
	}
	ts, err := s.st.GoTasks(sess.ID)
	if err != nil {
		return
	}
	cur := map[int64]bool{}
	for _, t := range ts {
		cur[t.ID] = true
		if notified[t.ID] {
			continue
		}
		notified[t.ID] = true
		var b strings.Builder
		fmt.Fprintf(&b, "Task #%d was set to go: %s\n", t.ID, t.Title)
		if strings.TrimSpace(t.Body) != "" {
			fmt.Fprintf(&b, "\n%s\n", t.Body)
		}
		fmt.Fprintf(&b, "\nStart it now: task_claim(id=%d) first (if that fails, ignore this event), then task_get, do the work, and task_done.", t.ID)
		s.send(notification{JSONRPC: "2.0", Method: "notifications/claude/channel", Params: map[string]any{
			"content": b.String(),
			"meta":    map[string]string{"task_id": strconv.FormatInt(t.ID, 10), "session": sess.Name},
		}})
	}
	// go から外れたものは忘れる（もう一度 go にされたらまた知らせる）
	for id := range notified {
		if !cur[id] {
			delete(notified, id)
		}
	}
}
