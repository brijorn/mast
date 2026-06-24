package scrcpy

import (
	_ "embed"
	"encoding/binary"
	"io"
)

const (
	ActionDown       = 0
	ActionUp         = 1
	ActionMove       = 2
	InjectTouchEvent = 2
	DefaultPressure  = 0xffff
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

func WriteSwipe(w io.Writer, startX, startY, endX, endY, width, height int) error {
	if err := writeTouch(w, ActionDown, startX, startY, width, height, DefaultPressure); err != nil {
		return err
	}
	if err := writeTouch(w, ActionMove, endX, endY, width, height, DefaultPressure); err != nil {
		return err
	}

	return writeTouch(w, ActionUp, endX, endY, width, height, 0)
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
