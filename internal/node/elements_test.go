package node

import "testing"

func TestParseAndroidElementsPreservesClickableCloseControl(t *testing.T) {
	data := []byte(`<hierarchy rotation="0"><node class="android.widget.FrameLayout" bounds="[0,0][1080,2340]" clickable="false" enabled="true"><node class="android.webkit.WebView" bounds="[0,80][1080,2205]" clickable="true" enabled="true"/><node class="android.widget.ImageButton" content-desc="Close" bounds="[810,2227][1080,2317]" clickable="true" enabled="true"/></node></hierarchy>`)
	elements, err := parseAndroidElements(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(elements) != 3 {
		t.Fatalf("elements=%+v", elements)
	}
	button := elements[2]
	if button.Label != "Close" || !button.Clickable || !button.Enabled {
		t.Fatalf("close button=%+v", button)
	}
	if button.Rect.X != 810 || button.Rect.Y != 2227 || button.Rect.Width != 270 || button.Rect.Height != 90 {
		t.Fatalf("close button rect=%+v", button.Rect)
	}
}
