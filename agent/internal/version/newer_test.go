package version

import "testing"

func TestNewer(t *testing.T) {
	yes := [][2]string{{"1.2.0", "1.1.9"}, {"v1.0.0", "1.0.0-rc.1"}, {"1.0.0-rc.10", "1.0.0-rc.2"}, {"2.0.0", "1.99.99"},
		{"1.0.0-rc.1", "1.0.0-beta"}, {"1.0.0-alpha.1", "1.0.0-alpha"}, {"1.10.0", "1.9.0"}}
	no := [][2]string{{"1.0.0", "1.0.0"}, {"1.0.0-rc.1", "1.0.0"}, {"x", "1.0.0"}, {"1.0.1", "0.0.0-dev"}, {"1.0.1", "0.0.0-ci"},
		{"1.0.0", "x"}, {"1.0", "0.9.0"}, {"1.0.0-", "0.9.0"}, {"01.0.0", "0.9.0"}, {"", "1.0.0"}, {"1.0.0-beta", "1.0.0-rc.1"}}
	for _, c := range yes {
		if !Newer(c[0], c[1]) {
			t.Errorf("Newer(%q, %q) = false, want true", c[0], c[1])
		}
	}
	for _, c := range no {
		if Newer(c[0], c[1]) {
			t.Errorf("Newer(%q, %q) = true, want false", c[0], c[1])
		}
	}
}
