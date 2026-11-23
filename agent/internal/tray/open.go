package tray

import (
	"errors"
	"net/url"
	"strings"
)

// checkURL accepts only http and https URLs with a host.
func checkURL(u string) error {
	if strings.ContainsAny(u, " \t\r\n") {
		return errors.New("tray: not a web address")
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" || p.Hostname() == "" {
		return errors.New("tray: not a web address")
	}
	return nil
}

// OpenURL opens an http or https address in the default browser. Anything
// else is refused and nothing is run.
func OpenURL(u string) error {
	if err := checkURL(u); err != nil {
		return err
	}
	return openURL(u)
}
