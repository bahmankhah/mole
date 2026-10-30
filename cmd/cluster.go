package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/resolver/crawler/app"
	"github.com/resolver/crawler/cluster"
	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/database"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/storage"
	"github.com/spf13/cobra"
)

var (
	workerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	regionPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

type syncOptions struct {
	allowVersionMismatch bool
}

func joinRequest(workerID string) cluster.JoinRequest {
	host, _ := os.Hostname()
	return cluster.JoinRequest{WorkerID: workerID, Hostname: host, Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// checkVersion refuses a control node of another version: a joined node does
// not migrate the schema, so it must run the control node's version.
func checkVersion(control string, allow bool) (warning string, err error) {
	if control == version {
		return "", nil
	}
	msg := fmt.Sprintf("this node runs mole %s but the control node runs %s", version, control)
	if allow {
		return msg, nil
	}
	return "", fmt.Errorf("%s. Upgrade this node by running the install command again (or install the control node's binary), or pass --allow-version-mismatch", msg)
}

// syncNodeConfig refreshes a joined node's configuration file from the control
// node. When the control node cannot be reached it falls back to the file from
// the last sync; when it refuses the node (revoked token) it fails.
func syncNodeConfig(path string, cfg *config.Config, o syncOptions) (*config.Config, error) {
	cl := cfg.Cluster
	controlURL, err := cluster.ValidateControlURL(cl.ControlURL, true)
	if err != nil {
		return nil, fmt.Errorf("cluster.control_url: %w", err)
	}
	node := cl.Node
	if node.ID == "" {
		node.ID = cfg.Worker.ID
	}
	if node.ID == "" {
		host, _ := os.Hostname()
		node.ID = cluster.NewNodeID(host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	resp, err := cluster.FetchConfig(ctx, controlURL, cl.JoinToken, joinRequest(node.ID))
	if err != nil {
		var rej *cluster.RejectedError
		if errors.As(err, &rej) {
			return nil, fmt.Errorf("%w; join again with a valid token: mole join %s --token <token>", err, controlURL)
		}
		if synced, _ := cluster.IsNodeFile(path); !synced {
			return nil, fmt.Errorf("%w (%s holds no earlier configuration to start with)", err, path)
		}
		log.Printf("[Cluster] %v; starting with the configuration from the last sync", err)
		return cfg, nil
	}
	warning, err := checkVersion(resp.Version, o.allowVersionMismatch)
	if err != nil {
		return nil, err
	}
	if warning != "" {
		log.Printf("[Cluster] Warning: %s", warning)
	}
	for _, w := range resp.Warnings {
		log.Printf("[Cluster] Warning: %s", w)
	}
	data, err := cluster.NodeFile(resp, controlURL, cl.JoinToken, node)
	if err != nil {
		return nil, err
	}
	if err := cluster.WriteFileAtomic(path, data); err != nil {
		return nil, fmt.Errorf("save configuration to %s: %w", path, err)
	}
	log.Printf("[Cluster] Configuration refreshed from %s", controlURL)
	return app.LoadConfig(path)
}

func newJoinCmd() *cobra.Command {
	var token, id, region string
	var maxJobs, pool int
	var noBrowser, allowHTTP, force, allowMismatch bool
	c := &cobra.Command{
		Use:   "join <control-url>",
		Short: "Make this machine a worker of a Mole control node",
		Long: `Fetches this node's configuration (database, storage, proxies, LLM, crawler
defaults…) from a control node running "mole serve", writes it to the config
file and checks that the database and storage are reachable from here. Start
the worker afterwards with "mole worker"; it refreshes the configuration from
the control node every time it starts.

Create a join token on the control node with "mole cluster token create" or on
its System page. Running join again keeps this node's id and other values
unless flags change them.`,
		Example: `  mole join https://mole.example.com --token mjt_… --region ir
  mole -c /etc/mole/config.yaml join http://10.8.0.1:5050 --token mjt_… --max-jobs 8`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("MOLE_JOIN_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("--token is required (create one on the control node: mole cluster token create)")
			}
			controlURL, err := cluster.ValidateControlURL(args[0], allowHTTP)
			if err != nil {
				return err
			}
			path := app.ConfigPath(flags.configPath)
			var node config.NodeSettings
			if _, err := os.Stat(path); err == nil {
				joined, err := cluster.IsNodeFile(path)
				if err != nil {
					return err
				}
				if !joined && !force {
					return fmt.Errorf("%s exists and is not a joined node's configuration; write elsewhere with -c <file> or replace it with --force", path)
				}
				if joined {
					if node, err = cluster.ReadNodeSettings(path); err != nil {
						return fmt.Errorf("read %s: %w", path, err)
					}
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if id != "" {
				node.ID = id
			}
			if region != "" {
				node.Region = region
			}
			if maxJobs > 0 {
				node.MaxRunningJobs = maxJobs
			}
			if pool > 0 {
				node.BrowserPoolSize = pool
			}
			if noBrowser {
				off := false
				node.Browser = &off
			}
			if node.ID == "" {
				host, _ := os.Hostname()
				node.ID = cluster.NewNodeID(host)
			}
			if !workerIDPattern.MatchString(node.ID) {
				return fmt.Errorf("--id may only contain letters, digits, '.', '_' and '-' (at most 128)")
			}
			if node.Region != "" && !regionPattern.MatchString(node.Region) {
				return fmt.Errorf("--region may only contain letters, digits, '.', '_' and '-' (at most 32)")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			resp, err := cluster.FetchConfig(ctx, controlURL, token, joinRequest(node.ID))
			if err != nil {
				return err
			}
			warning, err := checkVersion(resp.Version, allowMismatch)
			if err != nil {
				return err
			}
			data, err := cluster.NodeFile(resp, controlURL, token, node)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := cluster.WriteFileAtomic(path, data); err != nil {
				return fmt.Errorf("save configuration to %s: %w", path, err)
			}
			cfg, err := app.LoadConfig(path)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Joined %s as worker %s (region %s)\n", controlURL, cfg.WorkerIdentity(), anyIfEmpty(cfg.Worker.Region))
			fmt.Printf("✓ Configuration written to %s\n", path)
			if warning != "" {
				fmt.Printf("! %s\n", warning)
			}
			for _, w := range resp.Warnings {
				fmt.Printf("! %s\n", w)
			}
			dbAddr := net.JoinHostPort(cfg.Database.Host, cfg.Database.Port)
			if err := pingDatabase(cfg.Database); err != nil {
				return fmt.Errorf("the database %s is not reachable from this machine: %v\n"+
					"Let this node reach MySQL (VPN or firewall rule) or set cluster.node_database_host on the control node, then run join again", dbAddr, err)
			}
			fmt.Printf("✓ Database %s reachable\n", dbAddr)
			if err := checkStorage(cfg.Storage, node.ID); err != nil {
				return fmt.Errorf("object storage (%s) does not work from this machine: %v", cfg.Storage.Driver, err)
			}
			fmt.Printf("✓ Object storage (%s) readable and writable\n", cfg.Storage.Driver)
			fmt.Printf("\nStart the worker with:\n  mole -c %s worker\n", path)
			return nil
		},
	}
	c.Flags().StringVar(&token, "token", "", "join token (default $MOLE_JOIN_TOKEN)")
	c.Flags().StringVar(&id, "id", "", "worker id (default: host name plus a random suffix; kept on re-join)")
	c.Flags().StringVar(&region, "region", "", "region tag, e.g. ir or intl (default: the token's region)")
	c.Flags().IntVar(&maxJobs, "max-jobs", 0, "concurrent crawl jobs on this node (default: the control node's worker.max_running_jobs)")
	c.Flags().IntVar(&pool, "browser-pool", 0, "headless browser processes on this node (default: the control node's headless.pool_size)")
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "never run jobs that need the headless browser on this node")
	c.Flags().BoolVar(&allowHTTP, "allow-http", false, "allow a plain-http control URL on a public address (secrets travel unencrypted)")
	c.Flags().BoolVar(&force, "force", false, "replace an existing config file that is not a joined node's")
	c.Flags().BoolVar(&allowMismatch, "allow-version-mismatch", false, "join although the control node runs another version")
	return c
}

func anyIfEmpty(s string) string {
	if s == "" {
		return "any"
	}
	return s
}

func pingDatabase(cfg config.DatabaseConfig) error {
	db, err := database.New(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	sqlDB, err := db.DB.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx)
}

func checkStorage(cfg config.StorageConfig, nodeID string) error {
	st, err := storage.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	prefix := "doctor/join-" + nodeID + "/"
	if err := st.Put(ctx, prefix+"check.txt", []byte("ok"), "text/plain"); err != nil {
		return err
	}
	if _, err := st.Get(ctx, prefix+"check.txt"); err != nil {
		return err
	}
	return st.DeletePrefix(ctx, prefix)
}

func newClusterCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cluster",
		Short: "Join tokens and worker nodes",
		Long: `Add machines as workers with join tokens, and cordon, drain or reactivate
workers. A cordoned worker finishes its running jobs but takes no new ones; a
draining worker also hands its running jobs back to the queue, so other workers
continue them. Both states survive restarts until "uncordon".`,
	}
	token := &cobra.Command{Use: "token", Short: "Create, list and revoke join tokens"}

	var name, region, url string
	var ttl time.Duration
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a join token and print the commands that add a node",
		Example: `  mole cluster token create --name "tehran-2" --region ir --ttl 24h
  mole cluster token create --url https://mole.example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			controlURL := url
			if controlURL == "" {
				controlURL = a.Config.Cluster.AdvertiseURL
			}
			guessed := controlURL == ""
			if guessed {
				host, _ := os.Hostname()
				controlURL = "http://" + net.JoinHostPort(host, a.Config.Server.Port)
			}
			if controlURL, err = cluster.ValidateControlURL(controlURL, true); err != nil {
				return err
			}
			tok, secret, err := a.Manager.CreateJoinToken(jobs.JoinTokenSpec{Name: name, Region: region, TTL: ttl})
			if err != nil {
				return err
			}
			install, join, docker := cluster.JoinCommands(controlURL, secret)
			_, warnings, cfgErr := cluster.NodeConfig(a.Config, tok.Region)
			if cfgErr != nil {
				warnings = append(warnings, "nodes cannot join yet: "+cfgErr.Error())
			}
			if jsonOutput() {
				return printJSON(map[string]interface{}{
					"token": tok, "secret": secret, "control_url": controlURL,
					"install_command": install, "join_command": join, "docker_command": docker, "warnings": warnings,
				})
			}
			fmt.Printf("Join token (shown only once):\n  %s\n\n", secret)
			fmt.Printf("id %s · region %s · expires %s\n\n", tok.ID, anyIfEmpty(tok.Region), expiry(tok.ExpiresAt))
			fmt.Printf("Add a node (installs mole, the headless browser and a systemd service):\n  %s\n\n", install)
			fmt.Printf("With mole already installed on the node:\n  %s\n\n", join)
			fmt.Printf("With Docker (image built from this repository):\n  %s\n", docker)
			if guessed {
				fmt.Printf("\n! %s is a guess; pass --url or set cluster.advertise_url to the address nodes use to reach this server\n", controlURL)
			}
			for _, w := range warnings {
				fmt.Printf("! %s\n", w)
			}
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "", "description, e.g. the machine it is for")
	create.Flags().StringVar(&region, "region", "", "region given to nodes that join with it (default: any)")
	create.Flags().DurationVar(&ttl, "ttl", 0, "validity, e.g. 24h (default: until revoked)")
	create.Flags().StringVar(&url, "url", "", "URL nodes use to reach this server (default cluster.advertise_url)")

	list := &cobra.Command{
		Use:   "list",
		Short: "List join tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			toks, err := a.Manager.ListJoinTokens()
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(toks)
			}
			t := newTable("ID", "PREFIX", "NAME", "REGION", "STATUS", "USES", "LAST USED", "CREATED")
			now := time.Now()
			for _, tk := range toks {
				status := "active"
				switch {
				case tk.RevokedAt != nil:
					status = "revoked"
				case !tk.Usable(now):
					status = "expired"
				case tk.ExpiresAt != nil:
					status = "until " + fmtTime(tk.ExpiresAt)
				}
				t.row(tk.ID, tk.Prefix+"…", orDash(tk.Name), anyIfEmpty(tk.Region), status, tk.UseCount, fmtTime(tk.LastUsedAt), fmtTime(&tk.CreatedAt))
			}
			t.flush()
			return nil
		},
	}
	revoke := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a join token (nodes that joined with it can no longer start)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			if err := a.Manager.RevokeJoinToken(args[0]); err != nil {
				return err
			}
			fmt.Printf("Revoked join token %s\n", args[0])
			return nil
		},
	}
	token.AddCommand(create, list, revoke)

	nodes := &cobra.Command{
		Use:   "nodes",
		Short: "List live workers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(false)
			if err != nil {
				return err
			}
			defer a.Close()
			workers := a.Manager.Workers()
			if jsonOutput() {
				return printJSON(workers)
			}
			t := newTable("WORKER", "HOST", "REGION", "STATE", "BROWSER", "JOBS", "VERSION", "LAST SEEN")
			for _, w := range workers {
				browser := "no"
				if w.Browser {
					browser = "yes"
				}
				ver := w.Version
				if w.VersionMismatch {
					ver += " (≠ " + version + ")"
				}
				t.row(w.ID, w.Hostname, anyIfEmpty(w.Region), w.State, browser, fmt.Sprintf("%d/%d", w.RunningJobs, w.MaxJobs),
					ver, time.Since(w.LastSeenAt).Round(time.Second).String()+" ago")
			}
			t.flush()
			if len(workers) == 0 {
				fmt.Println("No worker has sent a heartbeat in the last 2 minutes.")
			}
			return nil
		},
	}

	setState := func(use, short, state string) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <worker-id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := openApp(false)
				if err != nil {
					return err
				}
				defer a.Close()
				if err := a.Manager.SetWorkerState(args[0], state); err != nil {
					return err
				}
				fmt.Printf("Worker %s is now %s (applied within a few seconds)\n", args[0], state)
				return nil
			},
		}
	}
	c.AddCommand(token, nodes,
		setState("cordon", "Stop a worker from taking new jobs; running jobs finish", models.WorkerCordoned),
		setState("drain", "Hand a worker's running jobs to other workers and stop it from taking new ones", models.WorkerDraining),
		setState("uncordon", "Let a cordoned or drained worker take jobs again", models.WorkerActive),
	)
	return c
}

func expiry(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return fmtTime(t)
}
