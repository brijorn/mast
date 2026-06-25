package cli

import "testing"

func TestNormalizeHTTPBaseAddsLocalhostForPortOnly(t *testing.T) {
	got, err := normalizeHTTPBase(":6271")
	if err != nil {
		t.Fatalf("normalizeHTTPBase returned error: %v", err)
	}

	if got != "http://127.0.0.1:6271" {
		t.Fatalf("base = %q", got)
	}
}

func TestNormalizeHTTPBaseAddsScheme(t *testing.T) {
	got, err := normalizeHTTPBase("100.64.0.1:6271")
	if err != nil {
		t.Fatalf("normalizeHTTPBase returned error: %v", err)
	}

	if got != "http://100.64.0.1:6271" {
		t.Fatalf("base = %q", got)
	}
}

func TestNormalizeHTTPBaseKeepsHTTPS(t *testing.T) {
	got, err := normalizeHTTPBase("https://mast.example.com/api/")
	if err != nil {
		t.Fatalf("normalizeHTTPBase returned error: %v", err)
	}

	if got != "https://mast.example.com/api" {
		t.Fatalf("base = %q", got)
	}
}
