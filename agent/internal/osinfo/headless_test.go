package osinfo

import "testing"

func TestHeadless(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, true},
		{map[string]string{"XDG_SESSION_TYPE": "x11"}, false},
		{map[string]string{"XDG_SESSION_TYPE": "wayland"}, false},
		{map[string]string{"XDG_SESSION_TYPE": "tty"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "ubuntu:GNOME"}, false},
		{map[string]string{"DISPLAY": ":10"}, true},
	}
	for _, c := range cases {
		if got := Headless(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: got %v want %v", c.env, got, c.want)
		}
	}
}
