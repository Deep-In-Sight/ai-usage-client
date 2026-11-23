package tray

import "os/exec"

func openURL(u string) error {
	cmd := exec.Command("/usr/bin/open", u)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
