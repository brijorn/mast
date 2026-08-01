package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/brijorn/mast/internal/transport"
)

// googleAccountType is the exact AccountManager type for a Google account.
// Other Google-published authenticators use types that merely share this
// prefix, so matching must be exact rather than by prefix.
const googleAccountType = "com.google"

// DeviceAccount is one Google account present on the device, carrying the
// Android user it belongs to. Accounts keep the order AccountManager reports,
// which is insertion order, so the first account of a user is its oldest.
type DeviceAccount struct {
	Email    string `json:"email"`
	UserID   int    `json:"user_id"`
	UserName string `json:"user_name,omitempty"`
}

type DeviceAccounts struct {
	Accounts []DeviceAccount `json:"accounts"`
}

func (n *Node) DeviceAccounts(serial string) (*DeviceAccounts, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.NodeID == n.ID {
		return n.localDeviceAccounts(serial)
	}
	return n.peerDeviceAccounts(n.ctx, device.NodeID, serial)
}

func (n *Node) localDeviceAccounts(serial string) (*DeviceAccounts, error) {
	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	switch device.Platform {
	case PlatformIOS:
		return nil, errors.New("device accounts are not supported for iOS devices")
	case PlatformAndroid:
	default:
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
	if device.State != "device" {
		return nil, fmt.Errorf("device %s is %s", serial, device.State)
	}

	output, err := n.adbShell(n.ctx, "", serial, "dumpsys", "account")
	if err != nil {
		return nil, err
	}

	return &DeviceAccounts{Accounts: parseDeviceAccounts(string(output))}, nil
}

// parseDeviceAccounts reads the per-user account listing at the head of
// `dumpsys account`. Later sections of that dump name accounts again in
// history, visibility, and authenticator forms; only the listing uses the
// `Account {name=..., type=...}` shape, so scanning for it cannot pick up a
// stale or duplicate entry.
func parseDeviceAccounts(output string) []DeviceAccount {
	accounts := []DeviceAccount{}
	userID := 0
	userName := ""
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if id, name, ok := parseAccountUserHeader(trimmed); ok {
			userID = id
			userName = name
			continue
		}
		email, accountType, ok := parseAccountEntry(trimmed)
		if !ok || accountType != googleAccountType {
			continue
		}
		accounts = append(accounts, DeviceAccount{Email: email, UserID: userID, UserName: userName})
	}
	return accounts
}

// parseAccountUserHeader reads `User UserInfo{0:Owner:4c13}:`.
func parseAccountUserHeader(line string) (int, string, bool) {
	const prefix = "User UserInfo{"
	if !strings.HasPrefix(line, prefix) {
		return 0, "", false
	}
	body := strings.TrimSuffix(strings.TrimSuffix(line[len(prefix):], ":"), "}")
	fields := strings.Split(body, ":")
	if len(fields) == 0 {
		return 0, "", false
	}
	id, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil {
		return 0, "", false
	}
	name := ""
	if len(fields) > 1 {
		name = strings.TrimSpace(fields[1])
	}
	return id, name, true
}

// parseAccountEntry reads `Account {name=a@b.com, type=com.google}`. The name
// is delimited from the right because an account name may itself contain a
// comma.
func parseAccountEntry(line string) (string, string, bool) {
	const prefix = "Account {"
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "}") {
		return "", "", false
	}
	body := strings.TrimSuffix(line[len(prefix):], "}")
	separator := strings.LastIndex(body, ", type=")
	if separator < 0 {
		return "", "", false
	}
	name := strings.TrimPrefix(body[:separator], "name=")
	accountType := body[separator+len(", type="):]
	if name == "" || accountType == "" {
		return "", "", false
	}
	return name, accountType, true
}

func (n *Node) peerDeviceAccounts(ctx context.Context, peerID string, serial string) (*DeviceAccounts, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.DeviceAccountsGetRequestPayload{Serial: serial}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeDeviceAccountsGetRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("device accounts from peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeDeviceAccountsGetResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DeviceAccountsGetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("device accounts from peer %s: %s", peerID, res.Payload.Error)
	}
	return deviceAccountsFromPayload(res.Payload.Result), nil
}

func (n *Node) handleDeviceAccountsGetRequest(peer *PeerConn, req transport.DeviceAccountsGetRequest) {
	accounts, err := n.localDeviceAccounts(req.Payload.Serial)
	payload := transport.DeviceAccountsGetResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = deviceAccountsPayload(accounts)
	}

	n.writePeerResponse(peer, transport.TypeDeviceAccountsGetResponse, req.RawMessage, payload)
}

func deviceAccountsPayload(accounts *DeviceAccounts) *transport.DeviceAccountsPayload {
	if accounts == nil {
		return nil
	}
	entries := make([]transport.DeviceAccountPayload, 0, len(accounts.Accounts))
	for _, account := range accounts.Accounts {
		entries = append(entries, transport.DeviceAccountPayload{
			Email:    account.Email,
			UserID:   account.UserID,
			UserName: account.UserName,
		})
	}
	return &transport.DeviceAccountsPayload{Accounts: entries}
}

func deviceAccountsFromPayload(payload *transport.DeviceAccountsPayload) *DeviceAccounts {
	if payload == nil {
		return nil
	}
	accounts := make([]DeviceAccount, 0, len(payload.Accounts))
	for _, entry := range payload.Accounts {
		accounts = append(accounts, DeviceAccount{
			Email:    entry.Email,
			UserID:   entry.UserID,
			UserName: entry.UserName,
		})
	}
	return &DeviceAccounts{Accounts: accounts}
}
