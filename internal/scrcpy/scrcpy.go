package scrcpy

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"io"
	"time"
	"unicode/utf8"
)

// Message types
const (
	InjectTouchEvent = 2
	InjectKeycode    = 0
	InjectText       = 1
	GetClipboard     = 8
	SetClipboard     = 9
	SetDisplayPower  = 10
)
const (
	DeviceMessageClipboard    = 0
	DeviceMessageAckClipboard = 1
	DeviceMessageUhidOutput   = 2
)
const (
	CopyKeyNone = 0
	CopyKeyCopy = 1
	CopyKeyCut  = 2
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
	KeycodeCopy      = 278
	KeycodePaste     = 279
)

var ValidKeycodes = map[int]bool{
	KeycodeBack:      true,
	KeycodeHome:      true,
	KeycodeAppSwitch: true,
	KeycodeCopy:      true,
	KeycodePaste:     true,

	// Numbers 0-9, *, #
	7:  true, // 0
	8:  true, // 1
	9:  true, // 2
	10: true, // 3
	11: true, // 4
	12: true, // 5
	13: true, // 6
	14: true, // 7
	15: true, // 8
	16: true, // 9
	17: true, // *
	18: true, // #

	// Dpad / Navigation
	19: true, // Up
	20: true, // Down
	21: true, // Left
	22: true, // Right

	// Letters A-Z
	29: true, 30: true, 31: true, 32: true, 33: true, 34: true, 35: true,
	36: true, 37: true, 38: true, 39: true, 40: true, 41: true, 42: true,
	43: true, 44: true, 45: true, 46: true, 47: true, 48: true, 49: true,
	50: true, 51: true, 52: true, 53: true, 54: true,

	// Punctuation and formatting
	55:  true, // Comma
	56:  true, // Period
	59:  true, // Shift Left
	60:  true, // Shift Right
	61:  true, // Tab
	62:  true, // Space
	66:  true, // Enter
	67:  true, // Del (Backspace)
	68:  true, // Grave
	69:  true, // Minus
	70:  true, // Equals
	71:  true, // Left bracket
	72:  true, // Right bracket
	73:  true, // Backslash
	74:  true, // Semicolon
	75:  true, // Apostrophe
	76:  true, // Slash
	77:  true, // At
	81:  true, // Plus
	111: true, // Escape
	112: true, // Forward Del (Delete)
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

func writeKeycode(w io.Writer, action byte, keycode uint32, metaState uint32) error {
	buf := make([]byte, 14)

	buf[0] = InjectKeycode
	buf[1] = action
	binary.BigEndian.PutUint32(buf[2:6], keycode)
	binary.BigEndian.PutUint32(buf[6:10], 0)          // repeat
	binary.BigEndian.PutUint32(buf[10:14], metaState) // meta state

	return writeFull(w, buf)
}

func PressKey(w io.Writer, keycode uint32, metaState uint32) error {
	if err := writeKeycode(w, ActionDown, keycode, metaState); err != nil {
		return err
	}
	return writeKeycode(w, ActionUp, keycode, metaState)
}

func WriteText(w io.Writer, text string) error {
	text = truncateUTF8(text, (1<<18)-5)
	rawText := []byte(text)

	buf := make([]byte, 5+len(rawText))
	buf[0] = InjectText
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(rawText)))
	copy(buf[5:], rawText)

	return writeFull(w, buf)
}

func WriteGetClipboard(w io.Writer, copyKey byte) error {
	return writeFull(w, []byte{GetClipboard, copyKey})
}

func WriteSetClipboard(w io.Writer, sequence uint64, text string, paste bool) error {
	text = truncateUTF8(text, (1<<18)-14)
	rawText := []byte(text)

	buf := make([]byte, 14+len(rawText))
	buf[0] = SetClipboard
	binary.BigEndian.PutUint64(buf[1:9], sequence)
	if paste {
		buf[9] = 1
	}
	binary.BigEndian.PutUint32(buf[10:14], uint32(len(rawText)))
	copy(buf[14:], rawText)

	return writeFull(w, buf)
}

func WriteSetDisplayPower(w io.Writer, on bool) error {
	value := byte(0)
	if on {
		value = 1
	}
	return writeFull(w, []byte{SetDisplayPower, value})
}

func ReadClipboardMessage(r io.Reader) (string, error) {
	for {
		header := make([]byte, 1)
		if _, err := io.ReadFull(r, header); err != nil {
			return "", err
		}

		switch header[0] {
		case DeviceMessageClipboard:
			sizeBuf := make([]byte, 4)
			if _, err := io.ReadFull(r, sizeBuf); err != nil {
				return "", err
			}
			size := binary.BigEndian.Uint32(sizeBuf)
			textBuf := make([]byte, size)
			if _, err := io.ReadFull(r, textBuf); err != nil {
				return "", err
			}
			return string(textBuf), nil

		case DeviceMessageAckClipboard:
			if _, err := io.CopyN(io.Discard, r, 8); err != nil {
				return "", err
			}

		case DeviceMessageUhidOutput:
			meta := make([]byte, 4)
			if _, err := io.ReadFull(r, meta); err != nil {
				return "", err
			}
			size := binary.BigEndian.Uint16(meta[2:4])
			if _, err := io.CopyN(io.Discard, r, int64(size)); err != nil {
				return "", err
			}

		default:
			return "", errors.New("unknown scrcpy device message")
		}
	}
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}

	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
