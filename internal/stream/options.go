package stream

import "strconv"

type Options struct {
	NoAudio   bool `json:"no_audio"`
	NoControl bool `json:"no_control"`
	// NoVideo asks for an interaction session: scrcpy's control socket alone,
	// with no virtual display and no encoder. A program that only injects
	// touches has no use for video, and encoding it costs the phone a hardware
	// encoder and a composited mirror for as long as the session lives.
	NoVideo bool `json:"no_video"`
	// MaxFPS caps how many frames a second the encoder produces; zero is
	// uncapped. A viewer that only glances at a phone, like a fleet board
	// tile, has no use for a full-rate stream, and encoding cost follows the
	// frame rate.
	MaxFPS              int    `json:"max_fps"`
	PreserveOrientation bool   `json:"preserve_orientation"`
	TurnScreenOff       bool   `json:"turn_screen_off"`
	StayAwake           bool   `json:"stay_awake"`
	MaxSize             int    `json:"max_size"`
	VideoBitrate        int    `json:"video_bitrate"`
	VideoCodecOptions   string `json:"video_codec_options"`
	DisablePowerOn      bool   `json:"-"`
	DisableCleanup      bool   `json:"-"`
}

// Defaults are the node-level encoder settings a stream falls back to. They
// exist so the encoder can be retuned through the config API without a Runway
// rebuild: a caller that omits a field gets the node's current value, and a
// caller that sets one still wins.
type Defaults struct {
	MaxSize           int
	VideoBitrate      int
	VideoCodecOptions string
}

func (s Options) WithDefaults(d Defaults) Options {
	if s.MaxSize == 0 {
		s.MaxSize = d.MaxSize
	}
	if s.VideoBitrate == 0 {
		s.VideoBitrate = d.VideoBitrate
	}
	if s.VideoCodecOptions == "" {
		s.VideoCodecOptions = d.VideoCodecOptions
	}
	return s
}

func (s *Options) Format() []string {
	var formatted []string
	if s.NoVideo {
		formatted = append(formatted, "video=false")
	}
	formatted = append(formatted,
		"audio="+strconv.FormatBool(!s.NoAudio),
		"control="+strconv.FormatBool(!s.NoControl),
		"stay_awake="+strconv.FormatBool(s.StayAwake),
		"clipboard_autosync=false",
	)
	if s.DisablePowerOn {
		formatted = append(formatted, "power_on=false")
	}
	if s.DisableCleanup {
		formatted = append(formatted, "cleanup=false")
	}
	if s.NoVideo {
		// Without video the first socket is the control socket, and the device
		// name scrcpy would send ahead of it is not a control message.
		return append(formatted, "send_device_meta=false")
	}
	if s.VideoCodecOptions != "" {
		formatted = append(formatted, "video_codec_options="+s.VideoCodecOptions)
	}
	if s.VideoBitrate > 0 {
		formatted = append(formatted, "video_bit_rate="+strconv.Itoa(s.VideoBitrate))
	}
	if s.MaxSize > 0 {
		formatted = append(formatted, "max_size="+strconv.Itoa(s.MaxSize))
	}
	if s.MaxFPS > 0 {
		formatted = append(formatted, "max_fps="+strconv.Itoa(s.MaxFPS))
	}
	return formatted
}
