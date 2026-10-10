package node

import (
	"slices"
	"testing"
)

const s22Getprop = `[ro.boot.hardware.color]: []
[ro.build.version.release]: [16]
[ro.product.brand]: [samsung]
[ro.product.device]: [r0q]
[ro.product.manufacturer]: [samsung]
[ro.product.model]: [SM-S901U1]
[ro.serialno]: [R5CT32NENWE]
[ril.product_code]: [SM-S901UIDAFKR]
[persist.sys.multi]: [line one
line two]
`

func TestAndroidDescriptionReadsSamsungProps(t *testing.T) {
	description := androidDescription("R5CT32NENWE", parseGetprop(s22Getprop))
	if description.Manufacturer != "samsung" || description.Model != "SM-S901U1" || description.Codename != "r0q" {
		t.Fatalf("unexpected identity: %+v", description)
	}
	if description.HardwareSerial != "R5CT32NENWE" || description.OSVersion != "16" {
		t.Fatalf("unexpected serial or OS: %+v", description)
	}
	if description.Color != "" || description.MarketingName != "" {
		t.Fatalf("Samsung records neither colour nor marketing name, got %+v", description)
	}
	if description.ProductCode != "SM-S901UIDAFKR" {
		t.Fatalf("product code = %q", description.ProductCode)
	}
}

func TestAndroidDescriptionReadsMotorolaColour(t *testing.T) {
	props := parseGetprop("[ro.product.model]: [moto g stylus 5G - 2024]\n[ro.boot.hardware.color]: [putty]\n[ro.boot.hardware.sku]: [XT2419-1]\n")
	description := androidDescription("ZD222X5ZGS", props)
	if description.Color != "putty" || description.SKU != "XT2419-1" {
		t.Fatalf("unexpected colour/SKU: %+v", description)
	}
}

func TestParseParcelStringReadsIMEI(t *testing.T) {
	// A real answer's shape; the digits are a made-up IMEI with a valid check digit.
	output := "Result: Parcel(\n" +
		"0x00000000: 00000000 0000000f 00350033 00320031 '........3.5.1.2.'\n" +
		"0x00000010: 00340033 00360035 00380037 00300039 '3.4.5.6.7.8.9.0.'\n" +
		"0x00000020: 00320031 00000034                   '1.2.4...        ')\n"
	value, err := parseParcelString(output)
	if err != nil {
		t.Fatal(err)
	}
	if value != "351234567890124" {
		t.Fatalf("value = %q", value)
	}
	if !validIMEI(value) {
		t.Fatalf("%q should pass the Luhn check", value)
	}
}

func TestParseParcelStringReportsException(t *testing.T) {
	output := "Result: Parcel(\n0x00000000: ffffffff 0000004d 00650052 00750071 '....M...R.e.q.u.'\n"
	if _, err := parseParcelString(output); err == nil {
		t.Fatal("a refused call must be an error, not an IMEI")
	}
}

func TestValidIMEIRejectsPlaceholders(t *testing.T) {
	for _, value := range []string{"", "000000000000001", "35123456789012", "A51234567890127", "351234567890128"} {
		if validIMEI(value) {
			t.Fatalf("%q should not be accepted as an IMEI", value)
		}
	}
	if !validIMEI("490154203237518") {
		t.Fatal("the textbook IMEI should pass")
	}
}

func TestIOSDescriptionReadsLockdownValues(t *testing.T) {
	description := iosDescription("00008110-000A1B2C3D4E5F6G", map[string]any{
		"ProductType":                           "iPhone14,6",
		"ProductVersion":                        "18.5",
		"SerialNumber":                          "F2LXK0ABCD12",
		"DeviceEnclosureColor":                  "8",
		"DeviceColor":                           "1",
		"ModelNumber":                           "MMX63",
		"RegionInfo":                            "LL/A",
		"DeviceName":                            "Biraj's iPhone",
		"InternationalMobileEquipmentIdentity":  "490154203237518",
		"InternationalMobileEquipmentIdentity2": "",
	})
	if description.Model != "iPhone14,6" || description.Manufacturer != "Apple" || description.OSVersion != "18.5" {
		t.Fatalf("unexpected identity: %+v", description)
	}
	if description.Color != "8" {
		t.Fatalf("the back's colour code should win, got %q", description.Color)
	}
	if description.SKU != "MMX63LL/A" || description.HardwareSerial != "F2LXK0ABCD12" {
		t.Fatalf("unexpected SKU/serial: %+v", description)
	}
	if !slices.Equal(description.IMEIs, []string{"490154203237518"}) {
		t.Fatalf("IMEIs = %v", description.IMEIs)
	}
}
