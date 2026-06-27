package stream

import "strconv"

type Options struct {
	NoAudio       bool `json:"no_audio"`
	NoControl     bool `json:"no_control"`
	TurnScreenOff bool `json:"turn_screen_off"`
	StayAwake     bool `json:"stay_awake"`
	MaxSize       int  `json:"max_size"`
	VideoBitrate  int  `json:"video_bitrate"`
}

func (s *Options) Format() []string {
	var formatted []string
	formatted = append(formatted,
		"audio="+strconv.FormatBool(!s.NoAudio),
		"control="+strconv.FormatBool(!s.NoControl),
		"stay_awake="+strconv.FormatBool(s.StayAwake),
	)
	if s.VideoBitrate > 0 {
		formatted = append(formatted, "video_bit_rate="+strconv.Itoa(s.VideoBitrate))
	}
	if s.MaxSize > 0 {
		formatted = append(formatted, "max_size="+strconv.Itoa(s.MaxSize))
	}
	return formatted
}
