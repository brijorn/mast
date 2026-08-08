package transport

import (
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	streamcfg "github.com/brijorn/mast/internal/stream"
)

const (
	TypeConnectionRequest            = "connection_request"
	TypeListDevicesRequest           = "list_devices_request"
	TypeListDevicesResponse          = "list_devices_response"
	TypeDeviceDNSGetRequest          = "device_dns_get_request"
	TypeDeviceDNSGetResponse         = "device_dns_get_response"
	TypeDeviceDNSSetRequest          = "device_dns_set_request"
	TypeDeviceDNSSetResponse         = "device_dns_set_response"
	TypeDeviceAccountsGetRequest     = "device_accounts_get_request"
	TypeDeviceAccountsGetResponse    = "device_accounts_get_response"
	TypeDeviceOrientationSetRequest  = "device_orientation_set_request"
	TypeDeviceOrientationSetResponse = "device_orientation_set_response"
	TypeDisplayPowerGetRequest       = "display_power_get_request"
	TypeDisplayPowerGetResponse      = "display_power_get_response"
	TypeDisplayPowerSetRequest       = "display_power_set_request"
	TypeDisplayPowerSetResponse      = "display_power_set_response"
	TypeScreenshotRequest            = "screenshot_request"
	TypeScreenshotResponse           = "screenshot_response"
	TypeElementsRequest              = "elements_request"
	TypeElementsResponse             = "elements_response"
	TypeStartStreamRequest           = "start_stream_request"
	TypeStartStreamResponse          = "start_stream_response"
	TypeStopStreamRequest            = "stop_stream_request"
	TypeTouchRequest                 = "touch_request"
	TypeTapRequest                   = "tap_request"
	TypeSwipeRequest                 = "swipe_request"
	TypePressKeyRequest              = "press_key_request"
	TypePressButtonRequest           = "press_button_request"
	TypeTextInputRequest             = "text_input_request"
	TypeLaunchAppRequest             = "launch_app_request"
	TypeOpenURLRequest               = "open_url_request"
	TypeClipboardGetRequest          = "clipboard_get_request"
	TypeClipboardGetResponse         = "clipboard_get_response"
	TypeClipboardSetRequest          = "clipboard_set_request"
	TypeUpdateCheckRequest           = "update_check_request"
	TypeUpdateCheckResponse          = "update_check_response"
	TypeUpdateApplyRequest           = "update_apply_request"
	TypeUpdateApplyResponse          = "update_apply_response"
	TypeConfigGetRequest             = "config_get_request"
	TypeConfigGetResponse            = "config_get_response"
	TypeConfigUpdateRequest          = "config_update_request"
	TypeConfigUpdateResponse         = "config_update_response"
)

type Message interface {
	MessageType() string
	MessageID() string
}

type RawMessage struct {
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Timestamp time.Time `json:"timestamp"`
}

func (msg *RawMessage) MessageType() string {
	return msg.Type
}

func (msg *RawMessage) MessageID() string {
	return msg.ID
}

type Heartbeat struct {
	RawMessage
}

type ConnectionRequestPayload struct {
	AndroidEnabled bool   `json:"android_enabled"`
	IOSEnabled     bool   `json:"ios_enabled"`
	ProxyEnabled   bool   `json:"proxy_enabled"`
	ADBPort        int    `json:"adb_port,omitempty"`
	APIAddr        string `json:"api_addr,omitempty"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuildDate      string `json:"build_date"`
}

type ConnectionRequest struct {
	RawMessage
	Payload ConnectionRequestPayload `json:"payload"`
}

type ListDevicesRequest struct {
	RawMessage
}

type DeviceInfoPayload struct {
	Serial   string                `json:"serial"`
	Address  string                `json:"address,omitempty"`
	Platform string                `json:"platform"`
	State    string                `json:"state"`
	NodeID   string                `json:"node_id"`
	Battery  *DeviceBatteryPayload `json:"battery,omitempty"`
}

type DeviceBatteryPayload struct {
	Percent *int   `json:"percent,omitempty"`
	State   string `json:"state"`
}

type ListDevicesResponsePayload struct {
	Result []DeviceInfoPayload `json:"result,omitempty"`
	Error  string              `json:"error,omitempty"`
}

type ListDevicesResponse struct {
	RawMessage
	Payload ListDevicesResponsePayload `json:"payload"`
}

type DeviceDNSStatusPayload struct {
	Mode     string `json:"mode"`
	Hostname string `json:"hostname,omitempty"`
}

type DeviceDNSGetRequestPayload struct {
	Serial string `json:"serial"`
}

type DeviceDNSGetRequest struct {
	RawMessage
	Payload DeviceDNSGetRequestPayload `json:"payload"`
}

type DeviceDNSGetResponsePayload struct {
	Result *DeviceDNSStatusPayload `json:"result,omitempty"`
	Error  string                  `json:"error,omitempty"`
}

type DeviceDNSGetResponse struct {
	RawMessage
	Payload DeviceDNSGetResponsePayload `json:"payload"`
}

type DeviceAccountPayload struct {
	Email    string `json:"email"`
	UserID   int    `json:"user_id"`
	UserName string `json:"user_name,omitempty"`
}

type DeviceAccountsPayload struct {
	Accounts []DeviceAccountPayload `json:"accounts"`
}

type DeviceAccountsGetRequestPayload struct {
	Serial string `json:"serial"`
}

type DeviceAccountsGetRequest struct {
	RawMessage
	Payload DeviceAccountsGetRequestPayload `json:"payload"`
}

type DeviceAccountsGetResponsePayload struct {
	Result *DeviceAccountsPayload `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

type DeviceAccountsGetResponse struct {
	RawMessage
	Payload DeviceAccountsGetResponsePayload `json:"payload"`
}

type DeviceDNSSetRequestPayload struct {
	Serial   string `json:"serial"`
	Mode     string `json:"mode"`
	Hostname string `json:"hostname,omitempty"`
}

type DeviceDNSSetRequest struct {
	RawMessage
	Payload DeviceDNSSetRequestPayload `json:"payload"`
}

type DeviceDNSSetResponsePayload struct {
	Result *DeviceDNSStatusPayload `json:"result,omitempty"`
	Error  string                  `json:"error,omitempty"`
}

type DeviceDNSSetResponse struct {
	RawMessage
	Payload DeviceDNSSetResponsePayload `json:"payload"`
}

type DeviceOrientationStatusPayload struct {
	Serial      string `json:"serial"`
	Platform    string `json:"platform"`
	Orientation string `json:"orientation"`
}

type DeviceOrientationSetRequestPayload struct {
	Serial      string `json:"serial"`
	Orientation string `json:"orientation"`
}

type DeviceOrientationSetRequest struct {
	RawMessage
	Payload DeviceOrientationSetRequestPayload `json:"payload"`
}

type DeviceOrientationSetResponsePayload struct {
	Result *DeviceOrientationStatusPayload `json:"result,omitempty"`
	Error  string                          `json:"error,omitempty"`
}

type DeviceOrientationSetResponse struct {
	RawMessage
	Payload DeviceOrientationSetResponsePayload `json:"payload"`
}

type DisplayPowerStatusPayload struct {
	Serial    string `json:"serial"`
	Platform  string `json:"platform"`
	Requested string `json:"requested"`
	Panel     string `json:"panel"`
	Policy    string `json:"policy"`
}

type DisplayPowerGetRequestPayload struct {
	Serial string `json:"serial"`
}

type DisplayPowerGetRequest struct {
	RawMessage
	Payload DisplayPowerGetRequestPayload `json:"payload"`
}

type DisplayPowerGetResponsePayload struct {
	Result *DisplayPowerStatusPayload `json:"result,omitempty"`
	Error  string                     `json:"error,omitempty"`
}

type DisplayPowerGetResponse struct {
	RawMessage
	Payload DisplayPowerGetResponsePayload `json:"payload"`
}

type DisplayPowerSetRequestPayload struct {
	Serial    string `json:"serial"`
	Requested string `json:"requested"`
}

type DisplayPowerSetRequest struct {
	RawMessage
	Payload DisplayPowerSetRequestPayload `json:"payload"`
}

type DisplayPowerSetResponsePayload struct {
	Result *DisplayPowerStatusPayload `json:"result,omitempty"`
	Error  string                     `json:"error,omitempty"`
}

type DisplayPowerSetResponse struct {
	RawMessage
	Payload DisplayPowerSetResponsePayload `json:"payload"`
}

type ScreenshotRequestPayload struct {
	Serial string `json:"serial"`
}

type ScreenshotRequest struct {
	RawMessage
	Payload ScreenshotRequestPayload `json:"payload"`
}

type ScreenshotResponsePayload struct {
	PNG   []byte `json:"png,omitempty"`
	Error string `json:"error,omitempty"`
}

type ScreenshotResponse struct {
	RawMessage
	Payload ScreenshotResponsePayload `json:"payload"`
}

type ElementsRequestPayload struct {
	Serial string `json:"serial"`
}

type ElementsRequest struct {
	RawMessage
	Payload ElementsRequestPayload `json:"payload"`
}

type ElementPayload struct {
	Type      string  `json:"type,omitempty"`
	Label     string  `json:"label,omitempty"`
	Value     string  `json:"value,omitempty"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	Clickable bool    `json:"clickable,omitempty"`
	Enabled   bool    `json:"enabled,omitempty"`
}

type ElementsResponsePayload struct {
	Result []ElementPayload `json:"result,omitempty"`
	Error  string           `json:"error,omitempty"`
}

type ElementsResponse struct {
	RawMessage
	Payload ElementsResponsePayload `json:"payload"`
}

type StartStreamRequestPayload struct {
	Serial  string            `json:"serial"`
	Options streamcfg.Options `json:"options"`
}
type StartStreamRequest struct {
	RawMessage
	Payload StartStreamRequestPayload `json:"payload"`
}

type StartStreamResultPayload struct {
	ID        string `json:"id"`
	Serial    string `json:"serial"`
	Platform  string `json:"platform"`
	Kind      string `json:"kind"`
	Host      string `json:"host"`
	LocalPort int    `json:"local_port"`
	VideoURL  string `json:"video_url,omitempty"`
	MJPEGURL  string `json:"mjpeg_url,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

type StartStreamResponsePayload struct {
	Result *StartStreamResultPayload `json:"result,omitempty"`
	Error  string                    `json:"error,omitempty"`
}

type StartStreamResponse struct {
	RawMessage
	Payload StartStreamResponsePayload `json:"payload"`
}

type StopStreamRequestPayload struct {
	Serial string `json:"serial"`
}

type StopStreamRequest struct {
	RawMessage
	Payload StopStreamRequestPayload `json:"payload"`
}

type TapRequest struct {
	RawMessage
	Payload TapRequestPayload `json:"payload"`
}

type TapRequestPayload struct {
	Serial string `json:"serial"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
}

type TouchRequest struct {
	RawMessage
	Payload TouchRequestPayload `json:"payload"`
}

type TouchRequestPayload struct {
	Serial    string  `json:"serial"`
	Action    string  `json:"action"`
	X         int     `json:"x"`
	Y         int     `json:"y"`
	PointerID *uint64 `json:"pointer_id,omitempty"`
}

type SwipeRequest struct {
	RawMessage
	Payload SwipeRequestPayload `json:"payload"`
}

type SwipeRequestPayload struct {
	Serial string `json:"serial"`
	StartX int    `json:"start_x"`
	StartY int    `json:"start_y"`
	EndX   int    `json:"end_x"`
	EndY   int    `json:"end_y"`
}

type PressKeyRequest struct {
	RawMessage
	Payload PressKeyRequestPayload `json:"payload"`
}

type PressKeyRequestPayload struct {
	Serial    string `json:"serial"`
	Keycode   uint32 `json:"keycode"`
	MetaState uint32 `json:"meta_state,omitempty"`
}

type PressButtonRequest struct {
	RawMessage
	Payload PressButtonRequestPayload `json:"payload"`
}

type PressButtonRequestPayload struct {
	Serial string `json:"serial"`
	Name   string `json:"name"`
}

type TextInputRequest struct {
	RawMessage
	Payload TextInputRequestPayload `json:"payload"`
}

type TextInputRequestPayload struct {
	Serial string `json:"serial"`
	Text   string `json:"text"`
}

type LaunchAppRequest struct {
	RawMessage
	Payload LaunchAppRequestPayload `json:"payload"`
}

type LaunchAppRequestPayload struct {
	Serial  string `json:"serial"`
	Package string `json:"package"`
}

type OpenURLRequest struct {
	RawMessage
	Payload OpenURLRequestPayload `json:"payload"`
}

type OpenURLRequestPayload struct {
	Serial string `json:"serial"`
	URL    string `json:"url"`
}

type ClipboardGetRequest struct {
	RawMessage
	Payload ClipboardGetRequestPayload `json:"payload"`
}

type ClipboardGetRequestPayload struct {
	Serial string `json:"serial"`
}

type ClipboardGetResponse struct {
	RawMessage
	Payload ClipboardGetResponsePayload `json:"payload"`
}

type ClipboardGetResponsePayload struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

type ClipboardSetRequest struct {
	RawMessage
	Payload ClipboardSetRequestPayload `json:"payload"`
}

type ClipboardSetRequestPayload struct {
	Serial string `json:"serial"`
	Text   string `json:"text"`
}

type UpdateCheckRequest struct {
	RawMessage
}

type UpdateCheckResultPayload struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	OS              string `json:"os"`
	Arch            string `json:"arch,omitempty"`
	AssetName       string `json:"asset_name,omitempty"`
	AssetURL        string `json:"asset_url,omitempty"`
	ChecksumURL     string `json:"checksum_url,omitempty"`
}

type UpdateApplyOptionsPayload struct {
	Force   bool `json:"force"`
	Restart bool `json:"restart"`
}

type UpdateApplyResultPayload struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	Updated         bool   `json:"updated"`
	RestartRequired bool   `json:"restart_required"`
	Message         string `json:"message"`
}

type UpdateCheckResponsePayload struct {
	Result *UpdateCheckResultPayload `json:"result,omitempty"`
	Error  string                    `json:"error,omitempty"`
}

type UpdateCheckResponse struct {
	RawMessage
	Payload UpdateCheckResponsePayload `json:"payload"`
}

type UpdateApplyRequest struct {
	RawMessage
	Payload UpdateApplyOptionsPayload `json:"payload"`
}

type UpdateApplyResponsePayload struct {
	Result *UpdateApplyResultPayload `json:"result,omitempty"`
	Error  string                    `json:"error,omitempty"`
}

type UpdateApplyResponse struct {
	RawMessage
	Payload UpdateApplyResponsePayload `json:"payload"`
}

type ConfigGetRequest struct {
	RawMessage
}

type ConfigGetResponsePayload struct {
	Config mastconfig.Config `json:"config"`
	Error  string            `json:"error,omitempty"`
}

type ConfigGetResponse struct {
	RawMessage
	Payload ConfigGetResponsePayload `json:"payload"`
}

type ConfigUpdateRequestPayload struct {
	Values map[string]string `json:"values"`
}

type ConfigUpdateRequest struct {
	RawMessage
	Payload ConfigUpdateRequestPayload `json:"payload"`
}

type ConfigUpdateResponsePayload struct {
	Result mastconfig.UpdateResult `json:"result"`
	Error  string                  `json:"error,omitempty"`
}

type ConfigUpdateResponse struct {
	RawMessage
	Payload ConfigUpdateResponsePayload `json:"payload"`
}
