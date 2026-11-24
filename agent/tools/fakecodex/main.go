// fakecodex is a stand-in for the Codex CLI in tests: it answers
// `codex --version` and speaks the part of `codex app-server --listen
// stdio://` the agent's fallback uses (JSON-RPC lines on stdin/stdout:
// initialize, initialized, account/rateLimits/read). Test tooling only,
// never shipped. Every value it sends is fake.
//
// The agent runs it with fixed arguments, so its own flags come from a
// file next to the executable, "<executable>.args" (one flag per line),
// so that each test can copy it with its own behaviour:
//
//	-mode ok|autherr|ratelimit|nomethod|hang|noexit|garbage|child
//	-reply FILE      the account/rateLimits/read result to send (ok)
//	-record FILE     append the arguments, CODEX_HOME and every stdin line
//	-pidfile FILE    write its process id (and, -mode child, the child's)
//	-delay D         wait D before answering the rate-limit read
//	-ignore-term     ignore SIGTERM (Unix), so only a kill ends it
//	-version TEXT    what --version prints (default "codex-cli 9.9.9")
//
// Modes: ok answers; autherr, ratelimit and nomethod answer with the
// JSON-RPC error codex gives in that case; hang never answers the read
// (it still ends when its input closes); noexit answers but keeps running
// after its input closes; garbage writes lines that are not JSON and
// ends; child starts a child process that sleeps, then behaves as hang
// and noexit together.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// defaultReply is an app-server rate-limit result with fake numbers: a
// 5-hour window at 42 % and a weekly one at 17 %.
const defaultReply = `{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","limitName":null,"primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1768046400},"secondary":{"usedPercent":17,"windowDurationMins":10080,"resetsAt":1768478400},"credits":null,"planType":"plus","rateLimitReachedType":null},"rateLimitsByLimitId":{"codex":{"limitId":"codex","limitName":null,"primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1768046400},"secondary":{"usedPercent":17,"windowDurationMins":10080,"resetsAt":1768478400},"credits":null,"planType":"plus","rateLimitReachedType":null}},"accountId":"00000000-0000-4000-8000-000000000001","rateLimitUpsell":null}`

type opts struct {
	mode, reply, record, pidfile, version string
	delay                                 time.Duration
	ignoreTerm                            bool
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout)) }

// fileArgs are the flags in "<executable>.args".
func fileArgs() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(exe + ".args")
	if err != nil {
		b, err = os.ReadFile(strings.TrimSuffix(exe, filepath.Ext(exe)) + ".args")
		if err != nil {
			return nil
		}
	}
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func run(args []string, in io.Reader, out io.Writer) int {
	if len(args) == 1 && args[0] == "-sleep" { // the child of -mode child
		time.Sleep(10 * time.Minute)
		return 0
	}
	var o opts
	fs := flag.NewFlagSet("fakecodex", flag.ContinueOnError)
	fs.StringVar(&o.mode, "mode", "ok", "")
	fs.StringVar(&o.reply, "reply", "", "")
	fs.StringVar(&o.record, "record", "", "")
	fs.StringVar(&o.pidfile, "pidfile", "", "")
	fs.StringVar(&o.version, "version", "codex-cli 9.9.9", "")
	fs.DurationVar(&o.delay, "delay", 0, "")
	fs.BoolVar(&o.ignoreTerm, "ignore-term", false, "")
	if err := fs.Parse(fileArgs()); err != nil {
		return 2
	}
	rec := func(s string) {
		if o.record == "" {
			return
		}
		f, err := os.OpenFile(o.record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, s)
			f.Close()
		}
	}
	rec("ARGS " + strings.Join(args, " "))
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(out, o.version)
		return 0
	}
	if len(args) != 3 || args[0] != "app-server" || args[1] != "--listen" || args[2] != "stdio://" {
		fmt.Fprintln(os.Stderr, "fakecodex: unexpected arguments")
		return 2
	}
	rec("CODEX_HOME " + os.Getenv("CODEX_HOME"))
	if o.ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	pids := []string{strconv.Itoa(os.Getpid())}
	hang, noexit := o.mode == "hang", o.mode == "noexit"
	if o.mode == "child" {
		self, _ := os.Executable()
		c := exec.Command(self, "-sleep")
		if err := c.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "fakecodex: child:", err)
			return 2
		}
		pids = append(pids, strconv.Itoa(c.Process.Pid))
		hang, noexit = true, true
	}
	if o.pidfile != "" {
		_ = os.WriteFile(o.pidfile, []byte(strings.Join(pids, " ")+"\n"), 0o600)
	}
	if o.mode == "garbage" {
		fmt.Fprintln(out, "this is not JSON")
		fmt.Fprintln(out, "{broken")
		return 0
	}
	reply := defaultReply
	if o.reply != "" {
		b, err := os.ReadFile(o.reply)
		if err != nil {
			return 2
		}
		reply = strings.TrimSpace(string(b))
	}
	send := func(v string) { fmt.Fprintln(out, v) }
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		rec("IN " + line)
		var m struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal([]byte(line), &m) != nil || m.ID == nil {
			continue
		}
		id := *m.ID
		switch m.Method {
		case "initialize":
			send(`{"method":"remoteControl/status/changed","params":{}}`)
			send(fmt.Sprintf(`{"id":%d,"result":{"userAgent":"codex_cli_rs/9.9.9 (fake; test) fakecodex","codexHome":"fake","platformFamily":"fake","platformOs":"fake"}}`, id))
		case "account/rateLimits/read":
			if o.delay > 0 {
				time.Sleep(o.delay)
			}
			switch o.mode {
			case "autherr":
				send(fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":"codex account authentication required to read rate limits"}}`, id))
			case "ratelimit":
				send(fmt.Sprintf(`{"id":%d,"error":{"code":-32603,"message":"failed to fetch codex rate limits: 429 Too Many Requests"}}`, id))
			case "nomethod":
				send(fmt.Sprintf(`{"id":%d,"error":{"code":-32601,"message":"Method not found"}}`, id))
			default:
				if hang {
					continue
				}
				send(`{"method":"account/updated","params":{}}`)
				send(fmt.Sprintf(`{"id":%d,"result":%s}`, id, reply))
			}
		default:
			send(fmt.Sprintf(`{"id":%d,"error":{"code":-32601,"message":"Method not found"}}`, id))
		}
	}
	rec("EOF")
	if noexit {
		for {
			time.Sleep(time.Hour)
		}
	}
	return 0
}
