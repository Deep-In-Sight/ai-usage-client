package autostart

func forOS(o Options) (Method, error) {
	return &RunKey{Exe: o.Exe, Start: o.Start}, nil
}

func othersOS(Options) []Method { return nil }
