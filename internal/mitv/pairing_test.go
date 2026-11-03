package mitv

import "testing"

func TestNormalizeName(t *testing.T) {
	for _, c := range []struct{ a, b string }{
		{"REDMI 显示器 G Pro 27U", "redmi显示器 gpro 27u"},
		{"ＲＥＤＭＩ　显示器", "redmi显示器"},
		{"Redmi\u00a0Monitor", "redmimonitor"},
	} {
		if NormalizeName(c.a) != NormalizeName(c.b) {
			t.Errorf("NormalizeName(%q) = %q, NormalizeName(%q) = %q", c.a, NormalizeName(c.a), c.b, NormalizeName(c.b))
		}
	}
}

func TestMatch(t *testing.T) {
	devices := []Device{
		{Host: "192.168.1.10", Name: "REDMI 显示器 G Pro 27U"},
		{Host: "192.168.1.11", Name: "REDMI 显示器 G Pro 27U"},
		{Host: "192.168.1.12", Name: "Xiaomi Monitor"},
	}
	if got := Match("REDMI 显示器 G Pro 27U", devices, nil); got != nil {
		t.Errorf("two same-named devices paired with %q", got.Host)
	}
	if got := Match("xiaomi monitor", devices, nil); got == nil || got.Host != "192.168.1.12" {
		t.Errorf("Xiaomi Monitor = %v", got)
	}
	if got := Match("Xiaomi Monitor", devices, map[string]bool{"192.168.1.12": true}); got != nil {
		t.Errorf("a taken device was paired: %q", got.Host)
	}
	if got := Match("", devices, nil); got != nil {
		t.Errorf("an unnamed monitor paired with %q", got.Host)
	}
	if got := Match("Some Other Monitor", devices, nil); got != nil {
		t.Errorf("an unknown monitor paired with %q", got.Host)
	}
	if got := Match("Xiaomi Monitor", nil, nil); got != nil {
		t.Errorf("Match with no devices = %v", got)
	}
}
