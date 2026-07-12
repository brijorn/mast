package program

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/brijorn/mast/internal/node"
)

var jsonWriteMu sync.Mutex

func writeJSON(path string, value any) error {
	jsonWriteMu.Lock()
	defer jsonWriteMu.Unlock()
	return writeJSONLocked(path, value)
}

func writeRunJSON(path string, run *Run) error {
	jsonWriteMu.Lock()
	defer jsonWriteMu.Unlock()
	if data, err := os.ReadFile(path); err == nil {
		var persisted Run
		if json.Unmarshal(data, &persisted) == nil && persisted.Revision > run.Revision {
			return nil
		}
	}
	return writeJSONLocked(path, run)
}

func writeJSONLocked(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = directory.Sync()
		closeErr := directory.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func writeJSONBestEffort(path string, value any) {
	if err := writeJSON(path, value); err != nil {
		log.Printf("persist %s: %v", path, err)
	}
}

func writeRunJSONBestEffort(path string, run *Run) {
	if err := writeRunJSON(path, run); err != nil {
		log.Printf("persist %s: %v", path, err)
	}
}

func hashDir(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(h, filepath.ToSlash(rel)+"\n"); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() {
			_ = f.Close()
		}()
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "", err
	}
	return "sha256-" + hex.EncodeToString(h.Sum(nil)), nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		_ = in.Close()
	}()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()
	_, err = io.Copy(out, in)
	return err
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func findDevice(devices []node.DeviceInfo, serial string) (node.DeviceInfo, bool) {
	for _, device := range devices {
		if device.Serial == serial {
			return device, true
		}
	}
	return node.DeviceInfo{}, false
}

func adbEnv(device node.DeviceInfo, nodes []node.NodeInfo) map[string]string {
	if device.Platform != node.PlatformAndroid {
		return map[string]string{}
	}

	env := map[string]string{
		"ANDROID_SERIAL": device.Serial,
	}
	for _, n := range nodes {
		if n.ID != device.NodeID || n.Local {
			continue
		}
		host, _ := splitHostPortDefault(n.Addr, DefaultADBPort)
		if host == "" {
			continue
		}
		port := n.ADBPort
		if port <= 0 {
			port = DefaultADBPort
		}
		env["ADB_SERVER_SOCKET"] = fmt.Sprintf("tcp:%s:%d", host, port)
		env["ANDROID_ADB_SERVER_ADDRESS"] = host
		env["ANDROID_ADB_SERVER_HOST"] = host
		env["ANDROID_ADB_SERVER_PORT"] = strconv.Itoa(port)
	}
	return env
}

func splitHostPortDefault(addr string, defaultPort int) (string, int) {
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimSuffix(addr, "/")
	host, portText, ok := strings.Cut(addr, ":")
	if !ok {
		return addr, defaultPort
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return host, defaultPort
	}
	return host, port
}

func mergeEnv(base []string, overlay map[string]string) []string {
	index := make(map[string]int)
	env := append([]string(nil), base...)
	for i, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			index[key] = i
		}
	}
	for key, value := range overlay {
		item := key + "=" + value
		if i, ok := index[key]; ok {
			env[i] = item
		} else {
			env = append(env, item)
		}
	}
	return env
}

func defaultRunEnv() map[string]string {
	return map[string]string{
		"PYTHONUNBUFFERED": "1",
	}
}

func withDefaultRunEnv(overrides map[string]string) map[string]string {
	env := defaultRunEnv()
	for key, value := range overrides {
		env[key] = value
	}
	return env
}

func applyConfigReplacements(path string, values []ConfigMapping, variables map[string]string, device node.DeviceInfo) error {
	if len(values) == 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)

	// 1. Global placeholder replace (e.g., replacing "{{license_key}}" inside config.py)
	for _, val := range values {
		var placeholders []string
		if val.Key != "" {
			placeholders = append(placeholders, "{{"+val.Key+"}}", "{{"+strings.ToLower(val.Key)+"}}")
		}
		if strings.HasPrefix(val.Value, "{{") && strings.HasSuffix(val.Value, "}}") {
			placeholders = append(placeholders, val.Value)
		}

		if len(placeholders) == 0 {
			continue
		}

		resolvedVal := val.Value
		varKey := val.Key
		if varKey == "" && strings.HasPrefix(val.Value, "{{") && strings.HasSuffix(val.Value, "}}") {
			varKey = strings.TrimSuffix(strings.TrimPrefix(val.Value, "{{"), "}}")
		}

		if varKey != "" {
			if v, ok := variables[varKey]; ok && v != "" {
				resolvedVal = v
			} else if v, ok := variables[strings.ToLower(varKey)]; ok && v != "" {
				resolvedVal = v
			}
		}

		resolved := resolveValue(resolvedVal, variables, device)
		for _, ph := range placeholders {
			content = strings.ReplaceAll(content, ph, resolved)
		}
	}

	// 2. Structured INI replacement (fallback for traditional .ini config files)
	if filepath.Ext(path) == ".ini" {
		content = renderINIValues(content, values, variables, device)
	}

	return os.WriteFile(path, []byte(content), 0600)
}

func renderINIValues(input string, values []ConfigMapping, variables map[string]string, device node.DeviceInfo) string {
	type sectionKey struct {
		section string
		key     string
	}
	replacements := make(map[sectionKey]string)
	for _, value := range values {
		resolvedVal := value.Value
		if v, ok := variables[value.Key]; ok && v != "" {
			resolvedVal = v
		} else if v, ok := variables[strings.ToLower(value.Key)]; ok && v != "" {
			resolvedVal = v
		}
		replacements[sectionKey{section: strings.ToLower(value.Section), key: strings.ToLower(value.Key)}] = resolveValue(resolvedVal, variables, device)
	}

	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(input))
	section := ""
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
			end := strings.Index(trimmed, "]")
			section = strings.ToLower(strings.TrimSpace(trimmed[1:end]))
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		replacement, ok := replacements[sectionKey{section: section, key: strings.ToLower(strings.TrimSpace(key))}]
		if !ok {
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		prefix := line[:strings.Index(line, "=")+1]
		out.WriteString(prefix)
		out.WriteString(" ")
		out.WriteString(replacement)
		out.WriteString("\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func resolveValue(value string, variables map[string]string, device node.DeviceInfo) string {
	current := value
	for i := 0; i < 5; i++ {
		next := replaceOnce(current, variables, device)
		if next == current {
			break
		}
		current = next
	}
	return current
}

func replaceOnce(val string, variables map[string]string, device node.DeviceInfo) string {
	var out strings.Builder
	pos := 0
	for {
		start := strings.Index(val[pos:], "{{")
		if start == -1 {
			out.WriteString(val[pos:])
			break
		}
		startIdx := pos + start
		end := strings.Index(val[startIdx:], "}}")
		if end == -1 {
			out.WriteString(val[pos:])
			break
		}
		endIdx := startIdx + end

		// Write prefix
		out.WriteString(val[pos:startIdx])

		// Extract placeholder name
		placeholder := strings.TrimSpace(val[startIdx+2 : endIdx])

		// Resolve placeholder
		var resolved string
		switch placeholder {
		case "phone.serial":
			resolved = device.Serial
		case "phone.node_id":
			resolved = device.NodeID
		default:
			if v, ok := variables[placeholder]; ok {
				resolved = v
			} else {
				// Keep the placeholder as is if not resolved
				resolved = val[startIdx : endIdx+2]
			}
		}

		out.WriteString(resolved)
		pos = endIdx + 2
	}
	return out.String()
}
