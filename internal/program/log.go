package program

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

func (s *Store) Logs(id string) (string, string, error) {
	logs, err := s.LogsSince(id, LogOffsets{})
	if err != nil {
		return "", "", err
	}
	return logs.Stdout, logs.Stderr, nil
}

func (s *Store) LogsSince(id string, offsets LogOffsets) (*LogsResult, error) {
	s.mu.Lock()
	state := s.runs[id]
	s.mu.Unlock()
	if state == nil {
		return nil, errors.New("run not found")
	}

	stdout, stdoutOffset, stdoutSize, stdoutReset, err := readLogFileSince(filepath.Join(state.run.Workspace, "stdout.log"), offsets.Stdout, state.run.StdoutLogStart)
	if err != nil {
		return nil, err
	}
	stderr, stderrOffset, stderrSize, stderrReset, err := readLogFileSince(filepath.Join(state.run.Workspace, "stderr.log"), offsets.Stderr, state.run.StderrLogStart)
	if err != nil {
		return nil, err
	}
	return &LogsResult{
		Stdout:       stdout,
		Stderr:       stderr,
		StdoutOffset: stdoutOffset,
		StderrOffset: stderrOffset,
		StdoutSize:   stdoutSize,
		StderrSize:   stderrSize,
		StdoutReset:  stdoutReset,
		StderrReset:  stderrReset,
	}, nil
}

type BoundedLogWriter = boundedLogWriter

type boundedLogWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	start    int64
	size     int64
	file     *os.File
	onTrim   func(start int64)
}

func (s *Store) newRunLogWriter(run *Run, path, stream string) (*boundedLogWriter, error) {
	if err := removeLogFiles(path); err != nil {
		return nil, err
	}
	return newBoundedLogWriter(path, runLogMaxBytes, func(start int64) {
		s.mu.Lock()
		switch stream {
		case "stdout":
			run.StdoutLogStart = start
		case "stderr":
			run.StderrLogStart = start
		}
		snapshot := nextRunSnapshot(run)
		s.mu.Unlock()
		writeRunJSONBestEffort(filepath.Join(snapshot.Workspace, "run.json"), &snapshot)
	})
}

func newBoundedLogWriter(path string, maxBytes int64, onTrim func(start int64)) (*boundedLogWriter, error) {
	if maxBytes <= 0 {
		maxBytes = runLogMaxBytes
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	return &boundedLogWriter{
		path:     path,
		maxBytes: maxBytes,
		file:     file,
		onTrim:   onTrim,
	}, nil
}

func (w *boundedLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	if err := w.trimLocked(); err != nil {
		return n, err
	}
	return n, nil
}

func (w *boundedLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *boundedLogWriter) trimLocked() error {
	if w.size <= w.maxBytes {
		return nil
	}
	trimBytes := w.size - w.maxBytes
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	in, err := os.Open(w.path)
	if err != nil {
		return err
	}
	if _, err := in.Seek(trimBytes, io.SeekStart); err != nil {
		_ = in.Close()
		return err
	}
	tail, err := io.ReadAll(in)
	_ = in.Close()
	if err != nil {
		return err
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, tail, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, w.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	w.file = file
	w.start += trimBytes
	w.size = int64(len(tail))
	if w.onTrim != nil {
		w.onTrim(w.start)
	}
	return nil
}

func readLogFileSince(path string, offset, start int64) (string, int64, int64, bool, error) {
	if offset < 0 {
		offset = 0
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", start, 0, offset > start, nil
		}
		return "", 0, 0, false, err
	}
	defer func() {
		_ = file.Close()
	}()

	info, err := file.Stat()
	if err != nil {
		return "", 0, 0, false, err
	}
	size := info.Size()
	end := start + size
	reset := false
	if offset < start || offset > end {
		offset = start
		reset = true
	}
	if _, err := file.Seek(offset-start, io.SeekStart); err != nil {
		return "", 0, 0, false, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", 0, 0, false, err
	}
	return string(data), end, size, reset, nil
}

func removeLogFiles(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(path + ".tmp"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
