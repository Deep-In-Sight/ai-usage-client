package tray

import "testing"

func TestOpenURLRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "javascript:alert(1)", "http://", "https:///path", "ftp://192.0.2.10/", "", "-flag", "http://192.0.2.10/\nx"} {
		if err := checkURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	for _, u := range []string{"http://claude-usage.local", "https://192.0.2.10:8700/install"} {
		if err := checkURL(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
}
