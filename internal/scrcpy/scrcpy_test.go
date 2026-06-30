package scrcpy

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestWriteTapWritesDownAndUpTouchEvents(t *testing.T) {
	var buf bytes.Buffer

	if err := WriteTap(&buf, 12, 34, 944, 1080); err != nil {
		t.Fatalf("WriteTap returned error: %v", err)
	}

	got := buf.Bytes()
	if len(got) != 64 {
		t.Fatalf("message length = %d, want %d", len(got), 64)
	}

	down := got[:32]
	up := got[32:]

	assertTouchEvent(t, down, ActionDown, 12, 34, 944, 1080, DefaultPressure)
	assertTouchEvent(t, up, ActionUp, 12, 34, 944, 1080, 0)
}

func TestWriteSwipeWritesDownMoveAndUpTouchEvents(t *testing.T) {
	var buf bytes.Buffer

	if err := writeSwipe(&buf, 12, 34, 56, 78, 944, 1080, 2, func(time.Duration) {}); err != nil {
		t.Fatalf("WriteSwipe returned error: %v", err)
	}

	got := buf.Bytes()
	if len(got) != 128 {
		t.Fatalf("message length = %d, want %d", len(got), 128)
	}

	down := got[:32]
	move := got[32:64]
	move2 := got[64:96]
	up := got[96:]

	assertTouchEvent(t, down, ActionDown, 12, 34, 944, 1080, DefaultPressure)
	assertTouchEvent(t, move, ActionMove, 34, 56, 944, 1080, DefaultPressure)
	assertTouchEvent(t, move2, ActionMove, 56, 78, 944, 1080, DefaultPressure)
	assertTouchEvent(t, up, ActionUp, 56, 78, 944, 1080, 0)
}

func TestWriteSetDisplayPowerWritesControlMessage(t *testing.T) {
	var off bytes.Buffer
	if err := WriteSetDisplayPower(&off, false); err != nil {
		t.Fatalf("WriteSetDisplayPower(false) returned error: %v", err)
	}
	if got, want := off.Bytes(), []byte{SetDisplayPower, 0}; !bytes.Equal(got, want) {
		t.Fatalf("off message = %v, want %v", got, want)
	}

	var on bytes.Buffer
	if err := WriteSetDisplayPower(&on, true); err != nil {
		t.Fatalf("WriteSetDisplayPower(true) returned error: %v", err)
	}
	if got, want := on.Bytes(), []byte{SetDisplayPower, 1}; !bytes.Equal(got, want) {
		t.Fatalf("on message = %v, want %v", got, want)
	}
}

func assertTouchEvent(t *testing.T, msg []byte, action byte, x, y, width, height int, pressure uint16) {
	t.Helper()

	if msg[0] != InjectTouchEvent {
		t.Fatalf("message type = %d, want %d", msg[0], InjectTouchEvent)
	}
	if msg[1] != action {
		t.Fatalf("action = %d, want %d", msg[1], action)
	}
	if got := binary.BigEndian.Uint64(msg[2:10]); got != ^uint64(1) {
		t.Fatalf("pointer id = %d, want %d", got, ^uint64(1))
	}
	if got := binary.BigEndian.Uint32(msg[10:14]); got != uint32(x) {
		t.Fatalf("x = %d, want %d", got, x)
	}
	if got := binary.BigEndian.Uint32(msg[14:18]); got != uint32(y) {
		t.Fatalf("y = %d, want %d", got, y)
	}
	if got := binary.BigEndian.Uint16(msg[18:20]); got != uint16(width) {
		t.Fatalf("width = %d, want %d", got, width)
	}
	if got := binary.BigEndian.Uint16(msg[20:22]); got != uint16(height) {
		t.Fatalf("height = %d, want %d", got, height)
	}
	if got := binary.BigEndian.Uint16(msg[22:24]); got != pressure {
		t.Fatalf("pressure = %d, want %d", got, pressure)
	}
	if got := binary.BigEndian.Uint32(msg[24:28]); got != 0 {
		t.Fatalf("action button = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint32(msg[28:32]); got != 0 {
		t.Fatalf("buttons = %d, want 0", got)
	}
}
