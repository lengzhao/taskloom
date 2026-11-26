// Command taskloom-admin is an offline, explicitly invoked bootstrap/recovery
// entry point. It never listens on HTTP or prints issued credentials. People
// authenticate only with a password, so bootstrap and recovery read it from
// stdin (never an argument) and issue nothing; only Agent enrollment writes a
// credential file.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	domain "github.com/lengzhao/taskloom/domain/security"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/plugins/security"
	"github.com/lengzhao/taskloom/runtime/config"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
func run(ctx context.Context, args []string, stdin io.Reader, out, errout io.Writer) int {
	f := flag.NewFlagSet("taskloom-admin", flag.ContinueOnError)
	f.SetOutput(errout)
	base := f.String("base", "base.yaml", "base configuration")
	overlay := f.String("config", "config.yaml", "deployment override")
	id := f.String("storage", "sql", "SQL resource instance ID")
	mode := f.String("mode", "", "bootstrap, recover or enroll-agent (local operator access)")
	tenant := f.String("tenant", "", "tenant ID")
	subject := f.String("subject", "", "administrator or Agent ID")
	operator := f.String("operator", "", "audited local operator")
	reason := f.String("reason", "", "audited reason")
	passwordStdin := f.Bool("password-stdin", false, "bootstrap and recover: read the administrator password from the first stdin line")
	path := f.String("credential-file", "", "enroll-agent: new private credential output file")
	owner := f.String("owner", "", "enroll-agent: human owner ID")
	ttl := f.Duration("ttl", 24*time.Hour, "enroll-agent: credential lifetime (maximum 360 days)")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	agent := *mode == "enroll-agent"
	if f.NArg() != 0 || (*mode != "bootstrap" && *mode != "recover" && !agent) || *tenant == "" || *subject == "" || *operator == "" || *reason == "" {
		fmt.Fprintln(errout, "explicit mode, tenant, subject, operator and reason required")
		return 2
	}
	// People sign in with a password and get no credential file; only an Agent
	// instance receives one.
	if agent && (*passwordStdin || *path == "" || *ttl <= 0 || *ttl > domain.MaxCredentialLifetime) {
		fmt.Fprintln(errout, "enroll-agent requires credential-file and a ttl within 360 days, and takes no password")
		return 2
	}
	if !agent && (!*passwordStdin || *path != "") {
		fmt.Fprintln(errout, "bootstrap and recover require -password-stdin and write no credential file")
		return 2
	}
	var password string
	if !agent {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		password = strings.TrimRight(line, "\r\n")
		if (err != nil && err != io.EOF) || !domain.ValidPassword(password) {
			fmt.Fprintln(errout, "a password of 8 to 128 bytes is required on stdin")
			return 2
		}
	}
	_, graph, err := config.BuildFiles(ctx, *base, *overlay)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	resource, ok := build.GetByID[lifecycle.Resource](graph, *id)
	if !ok {
		fmt.Fprintln(errout, "storage resource missing")
		return 1
	}
	// The administrative commands are the default graph's "security" instance.
	svc, ok := build.GetByID[*security.Service](graph, "security")
	if !ok {
		fmt.Fprintln(errout, "security instance missing")
		return 1
	}
	if !agent {
		if err = resource.Open(ctx); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		defer resource.Close(context.Background())
		in := security.AdminInput{Tenant: *tenant, Subject: *subject, Operator: *operator, Reason: *reason, Password: password}
		if *mode == "bootstrap" {
			err = svc.Bootstrap(ctx, in)
		} else {
			err = svc.Recover(ctx, in)
		}
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		fmt.Fprintln(out, "administrator password set; sign in with it")
		return 0
	}
	// Reserve the destination before mutating the database, never overwrite it.
	file, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(errout, "cannot create new credential file")
		return 1
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(*path)
		}
	}()
	if err = resource.Open(ctx); err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	defer resource.Close(context.Background())
	issued, err := svc.EnrollAgent(ctx, security.AgentEnrollment{Tenant: *tenant, Subject: *subject, Owner: *owner, Operator: *operator, Reason: *reason, ExpiresAt: time.Now().Add(*ttl)})
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	committed = true
	if err = json.NewEncoder(file).Encode(issued); err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = file.Close()
	}
	if err != nil {
		fmt.Fprintln(errout, "database committed but credential file could not be saved; enroll again with a new output path")
		return 1
	}
	fmt.Fprintln(out, "Agent credential saved to the requested private file")
	return 0
}
