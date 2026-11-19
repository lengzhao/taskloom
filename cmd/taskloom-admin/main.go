// Command taskloom-admin is an offline, explicitly invoked bootstrap/recovery
// entry point. It never listens on HTTP or prints issued credentials.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"io"
	"os"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/domain/security"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/runtime/config"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }
func run(ctx context.Context, args []string, out, errout io.Writer) int {
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
	path := f.String("credential-file", "", "new private credential output file")
	owner := f.String("owner", "", "human owner ID for Agent enrollment")
	instance := f.String("instance", "", "runtime instance ID")
	incarnation := f.String("incarnation", "", "runtime boot/incarnation ID")
	purpose := f.String("purpose", "business", "business or runtime credential")
	ttl := f.Duration("ttl", 24*time.Hour, "credential lifetime (maximum 90 days)")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || (*mode != "bootstrap" && *mode != "recover" && *mode != "enroll-agent") || *path == "" || *tenant == "" || *subject == "" || *operator == "" || *reason == "" || *ttl <= 0 || *ttl > 90*24*time.Hour {
		fmt.Fprintln(errout, "explicit mode, tenant, subject, operator, reason and credential-file required; ttl must be within 90 days")
		return 2
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
	tx, ok1 := build.GetByID[unitofwork.Manager](graph, *id)
	identities, ok2 := build.GetByID[identity.Principals](graph, *id)
	members, ok3 := build.GetByID[authorization.MemberStore](graph, *id)
	audit, ok4 := build.GetByID[record.AuditWriter](graph, *id)
	events, ok5 := build.GetByID[record.EventWriter](graph, *id)
	credentials, ok6 := build.GetByID[identity.Credentials](graph, *id)
	bootstrap, ok7 := build.GetByID[identity.BootstrapClaim](graph, *id)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !ok7 {
		fmt.Fprintln(errout, "storage lacks administrative capabilities")
		return 1
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
	svc := security.New(security.Deps{Transactions: tx, Principals: identities, Credentials: credentials, Bootstrap: bootstrap, Members: members, Audit: audit, Events: events})
	in := security.BootstrapInput{Tenant: *tenant, Subject: *subject, Operator: *operator, Reason: *reason, ExpiresAt: time.Now().Add(*ttl)}
	var issued capsecurity.Issued
	if *mode == "enroll-agent" {
		issued, err = svc.EnrollAgent(ctx, security.AgentEnrollment{BootstrapInput: in, Owner: *owner, InstanceID: *instance, Incarnation: *incarnation, Purpose: *purpose})
	} else if *mode == "bootstrap" {
		issued, err = svc.Bootstrap(ctx, in)
	} else {
		issued, err = svc.Recover(ctx, in)
	}
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
		fmt.Fprintln(errout, "database committed but credential file could not be saved; use offline recover with a new output path")
		return 1
	}
	fmt.Fprintln(out, "administrator credential saved to the requested private file")
	return 0
}
