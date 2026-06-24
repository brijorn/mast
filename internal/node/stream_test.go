package node

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestReadScrcpyVideoMetadata(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	go func() {
		deviceName := make([]byte, 64)
		copy(deviceName, "A15")

		streamMeta := make([]byte, 16)
		copy(streamMeta[:4], "h264")
		binary.BigEndian.PutUint32(streamMeta[4:8], 0x80000000)
		binary.BigEndian.PutUint32(streamMeta[8:12], 944)
		binary.BigEndian.PutUint32(streamMeta[12:16], 1080)

		_, _ = client.Write(deviceName)
		_, _ = client.Write(streamMeta)
	}()

	name, width, height, err := readScrcpyVideoMetadata(server)
	if err != nil {
		t.Fatalf("readScrcpyVideoMetadata returned error: %v", err)
	}

	if name != "A15" {
		t.Fatalf("name = %q, want %q", name, "A15")
	}
	if width != 944 {
		t.Fatalf("width = %d, want %d", width, 944)
	}
	if height != 1080 {
		t.Fatalf("height = %d, want %d", height, 1080)
	}
}
