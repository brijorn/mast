package node

import (
	"testing"

	streamcfg "github.com/brijorn/mast/internal/stream"
)

// A viewer needs video an interaction session cannot add, and a program needs
// only control, which any session gives it.
func TestEnsureStreamMatchesInteractionAndVideoCallers(t *testing.T) {
	cases := []struct {
		name     string
		existing *StreamSession
		opts     streamcfg.Options
		// replaced: a new session was started, closing the old one's
		// control clients. controlWithoutVideo: the session handed back may be
		// reaped once no viewer watches.
		replaced, controlWithoutVideo bool
	}{
		{
			name:     "viewer over an interaction session",
			existing: &StreamSession{Platform: PlatformAndroid, Kind: interactionSessionKind},
			replaced: true, controlWithoutVideo: true,
		},
		{
			name:     "program over a viewer's video session",
			existing: &StreamSession{Platform: PlatformAndroid, Kind: "h264", videoBroadcaster: newVideoBroadcaster()},
			opts:     streamcfg.Options{NoVideo: true}, controlWithoutVideo: true,
		},
		{
			name:     "program over an interaction session",
			existing: &StreamSession{Platform: PlatformAndroid, Kind: interactionSessionKind},
			opts:     streamcfg.Options{NoVideo: true},
		},
		{
			name:     "viewer over a viewer's video session",
			existing: &StreamSession{Platform: PlatformAndroid, Kind: "h264", videoBroadcaster: newVideoBroadcaster()},
		},
	}
	for _, tc := range cases {
		n := &Node{streams: map[string]*streamEntry{}}
		n.streams["phone"] = startedEntry(tc.existing)
		clientClosed := false
		release := n.AttachControlClient("phone", func() { clientClosed = true })
		fresh := &StreamSession{Platform: PlatformAndroid, Kind: "h264", videoBroadcaster: newVideoBroadcaster()}

		got, err := n.ensureStream("phone", tc.opts, func(string, streamcfg.Options) (*StreamSession, error) {
			return fresh, nil
		})
		release()

		if err != nil {
			t.Fatalf("%s: ensureStream: %v", tc.name, err)
		}
		want := tc.existing
		if tc.replaced {
			want = fresh
		}
		if got != want {
			t.Errorf("%s: replaced = %v, want %v", tc.name, got == fresh, tc.replaced)
		}
		if clientClosed != tc.replaced {
			t.Errorf("%s: control client closed = %v, want %v", tc.name, clientClosed, tc.replaced)
		}
		if got.controlWithoutVideo.Load() != tc.controlWithoutVideo {
			t.Errorf("%s: controlWithoutVideo = %v, want %v", tc.name, got.controlWithoutVideo.Load(), tc.controlWithoutVideo)
		}
	}
}

func TestParseAndroidDisplaySizeReadsTheDefaultDisplay(t *testing.T) {
	cases := []struct {
		name          string
		dump          string
		width, height int
		ok            bool
	}{
		{
			// scrcpy's mirror is listed first and is not the phone's screen.
			name: "portrait behind a scrcpy virtual display",
			dump: `  Display: mDisplayId=4
    init=332x720 1dpi mMinSizeOfResizeableTaskDp=200 cur=332x720 app=332x720 rng=332x332-720x720
  Display: mDisplayId=0 (organized)
    init=1080x2400 400dpi mMinSizeOfResizeableTaskDp=200 base=1080x2340 400dpi cur=1080x2340 app=1080x2340 rng=1080x1080-2340x2340
`,
			width: 1080, height: 2340, ok: true,
		},
		{
			name: "landscape",
			dump: `  Display: mDisplayId=0
    init=1080x2340 450dpi cur=2340x1080 app=2205x1080 rng=1080x1017-2340x2277
`,
			width: 2340, height: 1080, ok: true,
		},
		{
			name: "no default display",
			dump: `  Display: mDisplayId=4
    init=332x720 1dpi cur=332x720 app=332x720
`,
		},
	}
	for _, tc := range cases {
		width, height, err := parseAndroidDisplaySize(tc.dump)
		if (err == nil) != tc.ok || width != tc.width || height != tc.height {
			t.Errorf("%s: got %dx%d, %v; want %dx%d, ok=%v", tc.name, width, height, err, tc.width, tc.height, tc.ok)
		}
	}
}
