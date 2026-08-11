package node

import "testing"

// Shaped after a real WDA source document: every node carries
// type/enabled/visible and a rectangle, and only some carry name and label.
const iosSampleSource = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication type="XCUIElementTypeApplication" name="Pocket Champs" label="Pocket Champs" enabled="true" visible="true" x="0" y="0" width="390" height="844" index="0">
  <XCUIElementTypeWindow type="XCUIElementTypeWindow" enabled="true" visible="true" x="0" y="0" width="390" height="844" index="0">
    <XCUIElementTypeOther type="XCUIElementTypeOther" enabled="true" visible="false" x="0" y="0" width="390" height="844" index="0">
      <XCUIElementTypeButton type="XCUIElementTypeButton" name="closeButton" label="Close" value="1" enabled="true" visible="true" x="16" y="64" width="44" height="44" index="0"/>
    </XCUIElementTypeOther>
    <XCUIElementTypeOther type="XCUIElementTypeOther" enabled="false" visible="false" x="0" y="0" width="0" height="0" index="1"/>
  </XCUIElementTypeWindow>
</XCUIElementTypeApplication>`

func TestParseIOSElementsFlattensTree(t *testing.T) {
	elements, err := parseIOSElements(iosSampleSource)
	if err != nil {
		t.Fatalf("parseIOSElements: %v", err)
	}
	// Application, window, the one Other with a rectangle, and the button. The
	// zero-sized Other is dropped.
	if len(elements) != 4 {
		t.Fatalf("got %d elements, want 4: %+v", len(elements), elements)
	}

	var button *DeviceElement
	for index := range elements {
		if elements[index].Identifier == "closeButton" {
			button = &elements[index]
		}
	}
	if button == nil {
		t.Fatalf("closeButton not found in %+v", elements)
	}
	if button.Type != "XCUIElementTypeButton" {
		t.Errorf("Type = %q, want XCUIElementTypeButton", button.Type)
	}
	if button.Label != "Close" {
		t.Errorf("Label = %q, want Close", button.Label)
	}
	if button.Rect != (ElementRect{X: 16, Y: 64, Width: 44, Height: 44}) {
		t.Errorf("Rect = %+v, want {16 64 44 44}", button.Rect)
	}
	if !button.Enabled || !button.Clickable {
		t.Errorf("Enabled/Clickable = %v/%v, want true/true", button.Enabled, button.Clickable)
	}
}

// A zero-sized node reported at the origin would be tapped as a real control in
// the top-left corner.
func TestParseIOSElementsSkipsZeroSizedNodes(t *testing.T) {
	elements, err := parseIOSElements(iosSampleSource)
	if err != nil {
		t.Fatalf("parseIOSElements: %v", err)
	}
	for _, element := range elements {
		if element.Rect.Width <= 0 || element.Rect.Height <= 0 {
			t.Fatalf("empty rect survived flattening: %+v", element)
		}
	}
}

func TestParseIOSElementsRejectsEmptyDocument(t *testing.T) {
	if _, err := parseIOSElements("  "); err == nil {
		t.Fatal("expected an error for an empty source document")
	}
}

// The identifier is what an automation matches on, so an Android element must
// carry its resource-id the way an iOS element carries its name.
func TestParseAndroidElementsKeepsResourceID(t *testing.T) {
	const document = `<?xml version="1.0" encoding="UTF-8"?>
<hierarchy rotation="0">
  <node class="android.widget.Button" resource-id="com.example:id/close" content-desc="Close" text="X" bounds="[16,64][60,108]" clickable="true" enabled="true"/>
</hierarchy>`
	elements, err := parseAndroidElements([]byte(document))
	if err != nil {
		t.Fatalf("parseAndroidElements: %v", err)
	}
	if len(elements) != 1 {
		t.Fatalf("got %d elements, want 1", len(elements))
	}
	if elements[0].Identifier != "com.example:id/close" {
		t.Errorf("Identifier = %q, want com.example:id/close", elements[0].Identifier)
	}
}
