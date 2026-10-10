package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/brijorn/mast/internal/transport"
	goios "github.com/danielpaulus/go-ios/ios"
)

// DeviceDescription is what a phone says about its own hardware: who made it,
// which model it is, its IMEIs, and the colour its maker recorded for it. A
// caller keeping an inventory reads it once, while the phone is connected, so
// the phone can still be named after it has gone.
//
// Every field is the phone's own word and may be empty: makers differ in what
// they record, colour above all. A field that could not be read for a reason
// worth knowing — an IMEI refused by the baseband, say — is reported in
// Warnings instead of being guessed.
//
// It is the wire payload itself rather than a mirror of it, since there is
// nothing to translate between what the phone says and what a peer forwards.
type DeviceDescription = transport.DeviceDescriptionPayload

func (n *Node) DescribeDevice(serial string) (*DeviceDescription, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.NodeID == n.ID {
		return n.localDescribeDevice(serial)
	}
	return n.peerDescribeDevice(n.ctx, device.NodeID, serial)
}

func (n *Node) localDescribeDevice(serial string) (*DeviceDescription, error) {
	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.State != "device" {
		return nil, fmt.Errorf("device %s is %s", serial, device.State)
	}
	switch device.Platform {
	case PlatformAndroid:
		return n.describeAndroidDevice(serial)
	case PlatformIOS:
		return describeIOSDevice(serial)
	default:
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
}

func (n *Node) describeAndroidDevice(serial string) (*DeviceDescription, error) {
	output, err := n.adbShell(n.ctx, "", serial, "getprop")
	if err != nil {
		return nil, err
	}
	description := androidDescription(serial, parseGetprop(string(output)))

	// `cmd phone get-imei` is refused to the shell user, but the subscriber
	// info service still answers the shell's own package on the Android
	// releases the fleet runs (15 and 16 verified).
	parcel, err := n.adbShell(n.ctx, "", serial, "service", "call", "iphonesubinfo", "1", "s16", "com.android.shell")
	if err != nil {
		description.Warnings = append(description.Warnings, "IMEI could not be read: "+err.Error())
		return description, nil
	}
	imei, err := parseParcelString(string(parcel))
	switch {
	case err != nil:
		description.Warnings = append(description.Warnings, "IMEI could not be read: "+err.Error())
	case !validIMEI(imei):
		description.Warnings = append(description.Warnings, fmt.Sprintf("IMEI answer %q is not a valid IMEI", imei))
	default:
		description.IMEIs = []string{imei}
	}
	return description, nil
}

func androidDescription(serial string, props map[string]string) *DeviceDescription {
	return &DeviceDescription{
		Serial:         serial,
		Platform:       string(PlatformAndroid),
		HardwareSerial: props["ro.serialno"],
		Manufacturer:   props["ro.product.manufacturer"],
		Brand:          props["ro.product.brand"],
		Model:          props["ro.product.model"],
		// Pixels and Motorolas record a marketing name under one of these;
		// Samsung records none, and the model number stands in.
		MarketingName: firstNonEmpty(
			props["ro.product.marketname"],
			props["ro.product.vendor.marketname"],
			props["ro.config.marketing_name"],
		),
		Codename:    props["ro.product.device"],
		OSVersion:   props["ro.build.version.release"],
		Color:       props["ro.boot.hardware.color"],
		SKU:         props["ro.boot.hardware.sku"],
		ProductCode: props["ril.product_code"],
	}
}

// parseGetprop reads `getprop` output, one `[name]: [value]` per line.
func parseGetprop(output string) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(line, "[") {
			continue
		}
		name, value, ok := strings.Cut(line[1:], "]: [")
		if !ok || !strings.HasSuffix(value, "]") {
			continue
		}
		props[name] = strings.TrimSpace(strings.TrimSuffix(value, "]"))
	}
	return props
}

// parseParcelString reads the String16 a `service call` returns:
//
//	Result: Parcel(
//	0x00000000: 00000000 0000000f 00350033 00340038 '........3.5.8.4.'
//
// The first word is the call's status, the second the string's length in
// UTF-16 units, and each later word packs two units with the first in its
// low half. A non-zero status is an exception, such as a refused permission.
func parseParcelString(output string) (string, error) {
	var words []uint32
	for _, line := range strings.Split(output, "\n") {
		_, rest, ok := strings.Cut(line, ": ")
		if !ok || !strings.HasPrefix(strings.TrimSpace(line), "0x") {
			continue
		}
		if quote := strings.Index(rest, "'"); quote >= 0 {
			rest = rest[:quote]
		}
		for _, field := range strings.Fields(rest) {
			word, err := strconv.ParseUint(field, 16, 32)
			if err != nil {
				return "", fmt.Errorf("unreadable parcel word %q", field)
			}
			words = append(words, uint32(word))
		}
	}
	if len(words) < 2 {
		return "", errors.New("the call returned no parcel")
	}
	if words[0] != 0 {
		return "", fmt.Errorf("the call raised exception %d", int32(words[0]))
	}
	length := int(int32(words[1]))
	if length < 0 {
		return "", errors.New("the call returned no value")
	}
	units := make([]rune, 0, len(words)*2)
	for _, word := range words[2:] {
		units = append(units, rune(word&0xffff), rune(word>>16))
	}
	if len(units) < length {
		return "", errors.New("the parcel is shorter than its string")
	}
	return string(units[:length]), nil
}

// validIMEI accepts fifteen digits whose last is the Luhn check digit, which
// is what every IMEI is; a shorter or failing answer is a placeholder or a
// MEID and is not stored as one.
func validIMEI(value string) bool {
	if len(value) != 15 {
		return false
	}
	sum := 0
	for index, char := range value {
		if char < '0' || char > '9' {
			return false
		}
		digit := int(char - '0')
		if index%2 == 1 {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}

var iosLockdownValues = func(udid string) (map[string]any, error) {
	entry, err := goios.GetDevice(udid)
	if err != nil {
		return nil, err
	}
	return goios.GetValuesPlist(entry)
}

func describeIOSDevice(udid string) (*DeviceDescription, error) {
	values, err := iosLockdownValues(udid)
	if err != nil {
		return nil, fmt.Errorf("read lockdown values: %w", err)
	}
	return iosDescription(udid, values), nil
}

// iosDescription reads lockdown's own values. Apple records no marketing
// name, so the product type (iPhone14,6) is the model; its colour is a code
// (DeviceEnclosureColor is the back, DeviceColor the front) that only Apple's
// per-model tables turn into a name, so the code is passed on as recorded.
func iosDescription(udid string, values map[string]any) *DeviceDescription {
	text := func(key string) string {
		value, _ := values[key].(string)
		return strings.TrimSpace(value)
	}
	description := &DeviceDescription{
		Serial:         udid,
		Platform:       string(PlatformIOS),
		HardwareSerial: text("SerialNumber"),
		Manufacturer:   "Apple",
		Brand:          "Apple",
		Model:          text("ProductType"),
		MarketingName:  "",
		Codename:       text("HardwareModel"),
		OSVersion:      text("ProductVersion"),
		Color:          firstNonEmpty(text("DeviceEnclosureColor"), text("DeviceColor")),
		SKU:            text("ModelNumber") + text("RegionInfo"),
		DeviceName:     text("DeviceName"),
	}
	for _, key := range []string{"InternationalMobileEquipmentIdentity", "InternationalMobileEquipmentIdentity2"} {
		if imei := text(key); validIMEI(imei) {
			description.IMEIs = append(description.IMEIs, imei)
		}
	}
	return description
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (n *Node) peerDescribeDevice(ctx context.Context, peerID string, serial string) (*DeviceDescription, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.DeviceDescribeRequestPayload{Serial: serial}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeDeviceDescribeRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("describe device on peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeDeviceDescribeResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DeviceDescribeResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("describe device on peer %s: %s", peerID, res.Payload.Error)
	}
	if res.Payload.Result == nil {
		return nil, fmt.Errorf("describe device on peer %s: empty answer", peerID)
	}
	return res.Payload.Result, nil
}

func (n *Node) handleDeviceDescribeRequest(peer *PeerConn, req transport.DeviceDescribeRequest) {
	description, err := n.localDescribeDevice(req.Payload.Serial)
	payload := transport.DeviceDescribeResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = description
	}

	n.writePeerResponse(peer, transport.TypeDeviceDescribeResponse, req.RawMessage, payload)
}
