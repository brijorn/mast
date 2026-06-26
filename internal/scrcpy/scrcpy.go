package scrcpy

import (
	_ "embed"
	"encoding/binary"
	"io"
	"time"
)

// Message types
const (
	InjectTouchEvent = 2
	InjectKeycode    = 0
)
const (
	ActionDown      = 0
	ActionUp        = 1
	ActionMove      = 2
	DefaultPressure = 0xffff
)

// Android keycodes for navigation
const (
	KeycodeBack      = 4
	KeycodeHome      = 3
	KeycodeAppSwitch = 187
)

var ValidKeycodes = map[int]bool{
	KeycodeBack:      true,
	KeycodeHome:      true,
	KeycodeAppSwitch: true,
}

const (
	DefaultSwipeDuration = 250 * time.Millisecond
	DefaultSwipeSteps    = 8
)

//go:embed scrcpy-server-v4.0.jar
var Server []byte

const Filename = "scrcpy-server-v4.0.jar"
const ServerVersion = "4.0"
const RemotePath = "/data/local/tmp/scrcpy-server.jar"
const DeviceSocket = "localabstract:scrcpy"

func writeFull(w io.Writer, buf []byte) error {
	n, err := w.Write(buf)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return io.ErrShortWrite
	}
	return nil
}

func WriteTap(w io.Writer, x, y, width, height int) error {
	if err := writeTouch(w, ActionDown, x, y, width, height, DefaultPressure); err != nil {
		return err
	}

	return writeTouch(w, ActionUp, x, y, width, height, 0)
}

func WriteTouch(w io.Writer, action byte, x, y, width, height int) error {
	pressure := uint16(DefaultPressure)
	if action == ActionUp {
		pressure = 0
	}

	return writeTouch(w, action, x, y, width, height, pressure)
}

func WriteSwipe(w io.Writer, startX, startY, endX, endY, width, height int) error {
	return writeSwipe(w, startX, startY, endX, endY, width, height, DefaultSwipeSteps, time.Sleep)
}

func writeSwipe(w io.Writer, startX, startY, endX, endY, width, height, steps int, sleep func(time.Duration)) error {
	if steps < 1 {
		steps = 1
	}

	if err := writeTouch(w, ActionDown, startX, startY, width, height, DefaultPressure); err != nil {
		return err
	}

	delay := DefaultSwipeDuration / time.Duration(steps)
	for i := 1; i <= steps; i++ {
		if sleep != nil {
			sleep(delay)
		}

		x := interpolate(startX, endX, i, steps)
		y := interpolate(startY, endY, i, steps)
		if err := writeTouch(w, ActionMove, x, y, width, height, DefaultPressure); err != nil {
			return err
		}
	}

	return writeTouch(w, ActionUp, endX, endY, width, height, 0)
}

func interpolate(start, end, step, steps int) int {
	return start + ((end - start) * step / steps)
}

func writeTouch(w io.Writer, action byte, x, y, width, height int, pressure uint16) error {
	buf := make([]byte, 32)

	buf[0] = InjectTouchEvent
	buf[1] = action
	binary.BigEndian.PutUint64(buf[2:10], ^uint64(1)) // uint64(-2), generic finger
	binary.BigEndian.PutUint32(buf[10:14], uint32(x))
	binary.BigEndian.PutUint32(buf[14:18], uint32(y))
	binary.BigEndian.PutUint16(buf[18:20], uint16(width))
	binary.BigEndian.PutUint16(buf[20:22], uint16(height))
	binary.BigEndian.PutUint16(buf[22:24], pressure)
	binary.BigEndian.PutUint32(buf[24:28], 0) // action button
	binary.BigEndian.PutUint32(buf[28:32], 0) // buttons

	return writeFull(w, buf)
}

func writeKeycode(w io.Writer, action byte, keycode uint32) error {
	buf := make([]byte, 14)

	buf[0] = InjectKeycode
	buf[1] = action
	binary.BigEndian.PutUint32(buf[2:6], keycode)
	binary.BigEndian.PutUint32(buf[6:10], 0)  // repeat
	binary.BigEndian.PutUint32(buf[10:14], 0) // meta state

	return writeFull(w, buf)

}

func PressKey(w io.Writer, keycode uint32) error {
	if err := writeKeycode(w, ActionDown, keycode); err != nil {
		return err
	}
	return writeKeycode(w, ActionUp, keycode)
}
