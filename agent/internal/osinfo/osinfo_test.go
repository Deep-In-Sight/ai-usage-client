package osinfo

import "testing"

func TestParseOSRelease(t *testing.T) {
	cases := map[string]string{
		"NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\n": "24.04",
		"VERSION_ID=12\n":                         "12",
		"# c\nVERSION_ID='3.20'\n":                "3.20",
		"NAME=x\n":                                "",
	}
	for in, want := range cases {
		if got := ParseOSRelease([]byte(in)); got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestCurrentHasFields(t *testing.T) {
	i := Current()
	switch i.OS {
	case "linux", "windows", "macos":
	default:
		t.Fatalf("OS %q", i.OS)
	}
	if i.Hostname == "" || i.OSUser == "" || i.Arch == "" {
		t.Fatalf("%+v", i)
	}
	t.Logf("os_version %q", i.OSVersion)
}
