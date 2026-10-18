package node

import (
	"testing"
	"time"
)

func TestOverServedStreamsRestartLowerOnlyWhenNoViewerNeedsTheRate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		maxFPS  int
		viewers []int // each viewer's wanted rate, zero for full rate
		held    time.Duration
		want    bool
	}{
		{name: "board watching a full-rate stream", maxFPS: 0, viewers: []int{2}, held: 2 * time.Minute, want: true},
		{name: "driver and board on a full-rate stream", maxFPS: 0, viewers: []int{2, 0}, held: 2 * time.Minute},
		{name: "board watching a board stream", maxFPS: 2, viewers: []int{2, 2}, held: 2 * time.Minute},
		{name: "board watching a faster capped stream", maxFPS: 30, viewers: []int{2}, held: 2 * time.Minute, want: true},
		{name: "driver just stepped back to the board", maxFPS: 0, viewers: []int{2}, held: 10 * time.Second},
		{name: "nobody watching", maxFPS: 0, held: 2 * time.Minute},
	}
	for _, tc := range cases {
		session := &StreamSession{Kind: "h264", videoBroadcaster: newVideoBroadcaster(), maxFPS: tc.maxFPS}
		for _, fps := range tc.viewers {
			defer session.addViewer(fps)()
		}
		n := &Node{streams: map[string]*streamEntry{"phone": startedEntry(session)}}

		n.overServedStreams(now.Add(-tc.held), fpsDowngradeGrace)
		stale := n.overServedStreams(now, fpsDowngradeGrace)

		if got := len(stale) == 1; got != tc.want {
			t.Errorf("%s: restart = %v, want %v", tc.name, got, tc.want)
		}
	}
}
