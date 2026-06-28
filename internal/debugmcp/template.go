package debugmcp

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"sort"
)

func init() {
	image.RegisterFormat("jpeg", "\xff\xd8", jpeg.Decode, jpeg.DecodeConfig)
}

type templateMatchResult struct {
	Matched        bool      `json:"matched"`
	Score          float64   `json:"score"`
	Threshold      float64   `json:"threshold"`
	TopLeft        point     `json:"top_left"`
	Center         point     `json:"center"`
	ScreenshotSize imageSize `json:"screenshot_size"`
	TemplateSize   imageSize `json:"template_size"`
}

type point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type imageSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type samplePoint struct {
	x int
	y int
	c color.RGBA
}

func templateMatch(screenshotPath, templatePath string, threshold float64) (templateMatchResult, error) {
	if threshold <= 0 {
		threshold = 0.92
	}

	screen, err := decodeRGBA(screenshotPath)
	if err != nil {
		return templateMatchResult{}, fmt.Errorf("decode screenshot: %w", err)
	}
	tmpl, err := decodeRGBA(templatePath)
	if err != nil {
		return templateMatchResult{}, fmt.Errorf("decode template: %w", err)
	}

	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	tw, th := tmpl.Bounds().Dx(), tmpl.Bounds().Dy()
	if tw == 0 || th == 0 {
		return templateMatchResult{}, fmt.Errorf("template is empty")
	}
	if tw > sw || th > sh {
		return templateMatchResult{}, fmt.Errorf("template is larger than screenshot")
	}

	samples := templateSamples(tmpl, 24)
	if len(samples) == 0 {
		return templateMatchResult{}, fmt.Errorf("template has no visible pixels")
	}

	best := templateMatchResult{
		Score:          -1,
		Threshold:      threshold,
		ScreenshotSize: imageSize{Width: sw, Height: sh},
		TemplateSize:   imageSize{Width: tw, Height: th},
	}
	candidateCutoff := threshold - 0.08
	if candidateCutoff < 0.60 {
		candidateCutoff = 0.60
	}

	for y := 0; y <= sh-th; y++ {
		for x := 0; x <= sw-tw; x++ {
			sampled := sampleScore(screen, samples, x, y)
			if sampled > best.Score {
				best.Score = sampled
				best.TopLeft = point{X: x, Y: y}
			}
			if sampled < candidateCutoff {
				continue
			}
			full := fullScore(screen, tmpl, x, y)
			if full > best.Score {
				best.Score = full
				best.TopLeft = point{X: x, Y: y}
			}
		}
	}

	best.Center = point{X: best.TopLeft.X + tw/2, Y: best.TopLeft.Y + th/2}
	best.Matched = best.Score >= threshold
	return best, nil
}

func decodeRGBA(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, format, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	if format == "" {
		return nil, fmt.Errorf("unknown image format")
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, draw.Src)
	return rgba, nil
}

func templateSamples(img *image.RGBA, max int) []samplePoint {
	bounds := img.Bounds()
	var points []samplePoint
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c.A < 16 {
				continue
			}
			points = append(points, samplePoint{x: x - bounds.Min.X, y: y - bounds.Min.Y, c: c})
		}
	}
	if len(points) <= max {
		return points
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].y == points[j].y {
			return points[i].x < points[j].x
		}
		return points[i].y < points[j].y
	})
	step := float64(len(points)-1) / float64(max-1)
	out := make([]samplePoint, 0, max)
	for i := 0; i < max; i++ {
		out = append(out, points[int(float64(i)*step)])
	}
	return out
}

func sampleScore(screen *image.RGBA, samples []samplePoint, offsetX, offsetY int) float64 {
	var diff int
	for _, p := range samples {
		diff += pixelDiff(screen.RGBAAt(offsetX+p.x, offsetY+p.y), p.c)
	}
	return 1 - float64(diff)/float64(len(samples)*255*3)
}

func fullScore(screen, tmpl *image.RGBA, offsetX, offsetY int) float64 {
	var diff int
	var count int
	bounds := tmpl.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			t := tmpl.RGBAAt(x, y)
			if t.A < 16 {
				continue
			}
			diff += pixelDiff(screen.RGBAAt(offsetX+x-bounds.Min.X, offsetY+y-bounds.Min.Y), t)
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return 1 - float64(diff)/float64(count*255*3)
}

func pixelDiff(a, b color.RGBA) int {
	return absInt(int(a.R)-int(b.R)) + absInt(int(a.G)-int(b.G)) + absInt(int(a.B)-int(b.B))
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
