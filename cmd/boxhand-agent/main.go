// Command boxhand-agent lets a Boxhand on another machine manage the tsbox daemon of this machine, like the
// Portainer agent. It has no UI and no accounts: one API token, its own TLS certificate, and only the
// routes that a remote environment needs.
//
//	boxhand-agent init          create certificate and a new token (prints the token once)
//	boxhand-agent fingerprint   print the certificate fingerprint
//	boxhand-agent [flags]       serve
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rif223/boxhand-agent/internal/activity"
	"github.com/rif223/boxhand-agent/internal/agent"
	"github.com/rif223/boxhand-agent/internal/api"
	"github.com/rif223/boxhand-agent/internal/envs"
	"github.com/rif223/boxhand-agent/internal/projects"
)

var version = "0.1.0-dev"

func main() {
	log.SetPrefix("boxhand-agent: ")
	cmd := ""
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "init" || args[0] == "fingerprint") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("boxhand-agent", flag.ExitOnError)
	listen := fs.String("listen", envOr("BOXHAND_AGENT_LISTEN", ":9001"), "address of the agent (always TLS)")
	host := fs.String("host", envOr("TSBOX_HOST", "unix:///run/tsbox/tsboxd.sock"), "address of the tsbox daemon")
	bin := fs.String("tsbox", envOr("BOXHAND_TSBOX", "tsbox"), "path to the tsbox CLI (up/down, check, doctor)")
	data := fs.String("data", envOr("BOXHAND_AGENT_DATA", defaultDataDir()), "data directory of the agent")
	showVersion := fs.Bool("version", false, "print the version")
	_ = fs.Parse(args)

	switch {
	case *showVersion:
		fmt.Println("boxhand-agent", version)
	case cmd == "init":
		initAgent(*data)
	case cmd == "fingerprint":
		_, fp, err := agent.EnsureCert(*data)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(fp)
	default:
		serve(*listen, *host, *bin, *data)
	}
}

func initAgent(dir string) {
	_, fp, err := agent.EnsureCert(dir)
	if err != nil {
		log.Fatal(err)
	}
	token, err := agent.NewToken(dir, api.TokenKey)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf(`Boxhand-Agent is ready. Add it in Boxhand under Settings > Environments > Add environment > Boxhand-Agent:

  Address      https://<this machine>:9001
  Token        %s
  Fingerprint  %s

The token is shown only now; running init again replaces it. Restart a running agent afterwards.
`, token, fp)
}

func serve(listen, host, bin, dir string) {
	key, err := agent.TokenKey(dir)
	if err != nil {
		log.Fatal(err)
	}
	cert, fp, err := agent.EnsureCert(dir)
	if err != nil {
		log.Fatal(err)
	}
	store, err := projects.Open(filepath.Join(dir, "projects.json"))
	if err != nil {
		log.Fatal(err)
	}
	environments := envs.New(bin, host)
	actLog, err := activity.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	srv := &api.Server{
		Envs: environments, Projects: store, Activity: actLog, History: projects.OpenHistory(dir),
		DataDir: dir, Version: version, Listen: listen, TLS: true, TokenKey: key,
	}
	hs := &http.Server{
		Addr:              listen,
		Handler:           api.Guard{}.Wrap(srv.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Printf("Boxhand-Agent %s listening on https://%s (daemon %s), fingerprint %s", version, listen, host, fp)
	if err := hs.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "boxhand-agent")
	}
	return ".boxhand-agent"
}
