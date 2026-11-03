package display

import "testing"

func TestIdentity(t *testing.T) {
	id := Identity{Vendor: 0x10ac, Model: 0x0a0a, Serial: 0}
	if got, want := id.String(), "10ac:0a0a:00000000"; got != want {
		t.Errorf("identity = %q, want %q", got, want)
	}
	id.Position = "37D8832A-2D66-02CA-B9F7-8F30A301B230"
	if got, want := id.String(), "10ac:0a0a:00000000@37D8832A-2D66-02CA-B9F7-8F30A301B230"; got != want {
		t.Errorf("positional identity = %q, want %q", got, want)
	}
	if got := baseOf(id.String()); got != "10ac:0a0a:00000000" {
		t.Errorf("baseOf(%q) = %q", id.String(), got)
	}
}

func TestIdentities(t *testing.T) {
	for _, c := range []struct {
		name string
		ids  []Identity
		want []string
	}{
		{"distinct panels", []Identity{{1, 2, 3, "a"}, {1, 2, 4, "b"}}, []string{"0001:0002:00000003", "0001:0002:00000004"}},
		{"same model, serial 0", []Identity{{1, 2, 0, "a"}, {1, 2, 0, "b"}}, []string{"0001:0002:00000000@a", "0001:0002:00000000@b"}},
		{"same panel twice", []Identity{{1, 2, 3, "a"}, {1, 2, 3, "b"}}, []string{"0001:0002:00000003@a", "0001:0002:00000003@b"}},
		{"no position", []Identity{{1, 2, 0, ""}, {1, 2, 0, ""}}, []string{"0001:0002:00000000", "0001:0002:00000000"}},
	} {
		got := identities(c.ids)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %v", c.name, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: identities = %v, want %v", c.name, got, c.want)
			}
		}
	}
}

func TestMatchIdentity(t *testing.T) {
	for _, c := range []struct {
		name   string
		online string
		saved  []string
		want   string
	}{
		{"exact", "0001:0002:00000003", []string{"0001:0002:00000003", "0001:0002:00000004"}, "0001:0002:00000003"},
		{"the only one of the panel", "0001:0002:00000003@newport", []string{"0001:0002:00000003@oldport"}, "0001:0002:00000003@oldport"},
		{"panel saved without a position", "0001:0002:00000003@port", []string{"0001:0002:00000003"}, "0001:0002:00000003"},
		{"two of the panel, neither matched", "0001:0002:00000003@c", []string{"0001:0002:00000003@a", "0001:0002:00000003@b"}, ""},
		{"new panel", "0001:0002:00000009", []string{"0001:0002:00000003"}, ""},
		{"nothing saved", "0001:0002:00000003", nil, ""},
	} {
		if got := MatchIdentity(c.online, c.saved); got != c.want {
			t.Errorf("%s: MatchIdentity(%q, %v) = %q, want %q", c.name, c.online, c.saved, got, c.want)
		}
	}
}

func TestParseEDID(t *testing.T) {
	b := make([]byte, 128)
	b[8], b[9] = 0x10, 0xac
	b[10], b[11] = 0x0a, 0x0a
	b[12], b[13], b[14], b[15] = 0x78, 0x56, 0x34, 0x12
	vendor, model, serial, ok := parseEDID(b)
	if !ok || vendor != 0x10ac || model != 0x0a0a || serial != 0x12345678 {
		t.Errorf("parseEDID = %#x %#x %#x %v", vendor, model, serial, ok)
	}
	if _, _, _, ok := parseEDID(make([]byte, 8)); ok {
		t.Error("a short EDID parsed")
	}
}

func TestOrderMonitors(t *testing.T) {
	monitors := []Monitor{
		{Name: "right", x: 1920},
		{Name: "main", x: 3840, Primary: true},
		{Name: "left", x: 0},
		{Name: "middle", x: 1920},
	}
	orderMonitors(monitors)
	var names []string
	for _, m := range monitors {
		names = append(names, m.Name)
	}
	want := []string{"main", "left", "right", "middle"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("order = %v, want %v", names, want)
		}
	}
}
