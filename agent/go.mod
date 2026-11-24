module github.com/Deep-In-Sight/ai-usage-client/agent

go 1.27

require (
	fyne.io/systray v1.12.2
	github.com/godbus/dbus/v5 v5.1.0
	github.com/miekg/dns v1.1.73
	golang.org/x/sys v0.50.0
)

require (
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/tc-hib/go-winres v0.3.3 // indirect
	github.com/tc-hib/winres v0.2.1 // indirect
	github.com/urfave/cli/v2 v2.25.7 // indirect
	github.com/xrash/smetrics v0.0.0-20201216005158-039620a65673 // indirect
	golang.org/x/image v0.12.0 // indirect
	golang.org/x/net v0.57.0 // indirect
)

tool github.com/tc-hib/go-winres

// A patched copy of fyne.io/systray v1.12.2; see third_party/systray/PATCHED.md.
replace fyne.io/systray => ./third_party/systray
