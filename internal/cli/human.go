package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/whosabhiraj/baton/internal/daemon"
	"github.com/whosabhiraj/baton/internal/gitx"
	"github.com/whosabhiraj/baton/internal/ids"
	"github.com/whosabhiraj/baton/internal/store"
)

// Seams for tests.
var (
	daemonDiscover = daemon.Discover
	daemonStart    = daemon.StartDetached
	daemonWait     = daemon.WaitRunning
	daemonStop     = daemon.Stop
	executable     = os.Executable
	openBrowser    = openURL
)

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// dashboardURL is the page to open: the admin token rides in the fragment, which the browser
// never sends to the server; the dashboard stores it and strips it from the address bar.
func dashboardURL(hubURL, token string) string {
	u := strings.TrimRight(hubURL, "/") + "/"
	if token != "" {
		u += "#token=" + token
	}
	return u
}

// ask prints a question on stderr and reads one line from stdin; only y/yes means yes (EOF = no).
func (c *ctx) ask(question string) bool {
	fmt.Fprint(c.stderr, question)
	line, _ := bufio.NewReader(c.stdin).ReadString('\n')
	// Windows PowerShell 5.1 prefixes piped text with a UTF-8 BOM ("y" | baton ...).
	line = strings.TrimPrefix(line, string(rune(0xFEFF)))
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// ensureRepo makes sure the working folder is inside a git repository (asking before git init).
// ok=false means the user declined (exit 2).
func (c *ctx) ensureRepo(o opts) (ok bool, err error) {
	if o.has("project") || o.has("data-dir") {
		return true, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	if gitx.IsUnsafeRoot(cwd) {
		fmt.Fprintln(c.stderr, "baton: refusing to use your home folder or a drive root as a project; cd into a project folder first")
		return false, nil
	}
	_, err = gitx.ProjectTopLevel(cwd)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gitx.ErrUnsafeRoot) {
		fmt.Fprintln(c.stderr, "baton: warning: ignoring a git repository at your home folder or a drive root")
	}
	if !c.ask(fmt.Sprintf("%s is not a git repository. Initialise one here? [y/N] ", cwd)) {
		fmt.Fprintln(c.stderr, "baton: not a git repository; nothing started")
		return false, nil
	}
	if err := gitx.Init(cwd); err != nil {
		return false, fmt.Errorf("git init: %w", err)
	}
	return true, nil
}

// ensureHub returns the running hub for the project, starting a detached one if needed.
func (c *ctx) ensureHub(o opts) (*daemon.Info, string, error) {
	repo, dataDir, err := project(o)
	if err != nil {
		return nil, "", err
	}
	in, st, err := daemonDiscover(dataDir)
	if err != nil {
		return nil, dataDir, err
	}
	switch st {
	case daemon.Running:
		return in, dataDir, nil
	case daemon.Starting:
		in, err = daemonWait(dataDir, daemon.StartWait)
		return in, dataDir, err
	}
	exe, err := executable()
	if err != nil {
		return nil, dataDir, err
	}
	args := []string{"hub", "serve", "--data-dir", dataDir}
	if repo != "" {
		args = append(args, "--project", repo)
	}
	if err := daemonStart(exe, args, dataDir); err != nil {
		return nil, dataDir, err
	}
	in, st, err = daemonDiscover(dataDir)
	if err == nil && st != daemon.Running {
		err = errors.New("hub is not running after start")
	}
	return in, dataDir, err
}

func readAdminToken(dataDir string) string {
	b, _ := os.ReadFile(adminTokenPath(dataDir))
	return strings.TrimSpace(string(b))
}

func (c *ctx) printHub(in *daemon.Info, dataDir string) {
	fmt.Fprintf(c.stdout, "baton hub: %s\n", in.URL)
	fmt.Fprintf(c.stdout, "dashboard: %s/\n", in.URL)
	fmt.Fprintf(c.stdout, "admin token: %s\n", readAdminToken(dataDir))
}

// cmdUpOrBaton implements `baton up`, `baton` (conductor=true) and `baton attach`.
func (c *ctx) cmdUpOrBaton(o opts, conductor, attach bool) (int, error) {
	if !attach {
		ok, err := c.ensureRepo(o)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 2, nil
		}
	}
	in, dataDir, err := c.ensureHub(o)
	if err != nil {
		return 0, err
	}
	c.printHub(in, dataDir)
	if attach {
		c.printStatusLines(in, dataDir)
		fmt.Fprintln(c.stdout, "conductor chat arrives in Phase 6")
		return 0, nil
	}
	if conductor {
		fmt.Fprintln(c.stdout, "conductor: arrives in Phase 6")
	}
	if !o.flag("no-browser") {
		repo, _, _ := project(o)
		if loadConfig(repo).Config.Hub.OpenBrowser {
			openBrowser(dashboardURL(in.URL, readAdminToken(dataDir)))
		}
	}
	return 0, nil
}

// liveRuns counts runs from GET /api/state that have not finished.
func liveRuns(in *daemon.Info, token string) (int, error) {
	req, _ := http.NewRequest("GET", strings.TrimRight(in.URL, "/")+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("GET /api/state: HTTP %d", resp.StatusCode)
	}
	var st struct {
		Runs []struct {
			Status string `json:"status"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range st.Runs {
		switch r.Status {
		case "done", "failed", "killed", "exited":
		default:
			n++
		}
	}
	return n, nil
}

func (c *ctx) printStatusLines(in *daemon.Info, dataDir string) {
	fmt.Fprintf(c.stdout, "pid: %d\n", in.PID)
	fmt.Fprintf(c.stdout, "version: %s\n", in.Version)
	fmt.Fprintf(c.stdout, "data dir: %s\n", dataDir)
	if n, err := liveRuns(in, readAdminToken(dataDir)); err == nil {
		fmt.Fprintf(c.stdout, "live runs: %d\n", n)
	} else {
		fmt.Fprintf(c.stdout, "live runs: unknown (%s)\n", err.Error())
	}
}

func (c *ctx) cmdHub(o opts, pos []string) (int, error) {
	if len(pos) == 0 {
		return 0, usagef("missing hub subcommand (serve|status|stop|rotate-token)")
	}
	if len(pos) > 1 {
		return 0, usagef("unexpected argument %q", pos[1])
	}
	switch pos[0] {
	case "serve":
		return c.cmdHubServe(o)
	case "status":
		_, dataDir, err := project(o)
		if err != nil {
			return 0, err
		}
		in, st, err := daemonDiscover(dataDir)
		if err != nil {
			return 0, err
		}
		if st == daemon.Running {
			fmt.Fprintln(c.stdout, "hub: running")
			fmt.Fprintf(c.stdout, "url: %s\n", in.URL)
			c.printStatusLines(in, dataDir)
		} else {
			fmt.Fprintf(c.stdout, "hub: %s\n", st)
			fmt.Fprintf(c.stdout, "data dir: %s\n", dataDir)
		}
		return 0, nil
	case "stop":
		return c.cmdHubStop(o)
	case "rotate-token":
		return c.cmdRotateToken(o)
	}
	return 0, usagef("unknown hub subcommand %q (serve|status|stop|rotate-token)", pos[0])
}

func (c *ctx) cmdHubStop(o opts) (int, error) {
	_, dataDir, err := project(o)
	if err != nil {
		return 0, err
	}
	in, st, err := daemonDiscover(dataDir)
	if err != nil {
		return 0, err
	}
	if st == daemon.Stopped {
		fmt.Fprintln(c.stdout, "hub: not running")
		return 0, nil
	}
	token := readAdminToken(dataDir)
	if st == daemon.Running && !o.flag("yes") {
		if n, err := liveRuns(in, token); err == nil && n > 0 {
			if !c.ask(fmt.Sprintf("%d agent runs are live. Stop the hub? [y/N] ", n)) {
				fmt.Fprintln(c.stderr, "baton: not stopped")
				return 1, nil
			}
		}
	}
	if o.flag("keep-agents") {
		fmt.Fprintln(c.stdout, "note: --keep-agents takes effect from Phase 5 (agents are not supervised yet)")
	}
	if err := daemonStop(dataDir, token, 10*time.Second); err != nil {
		return 0, err
	}
	fmt.Fprintln(c.stdout, "hub: stopped")
	return 0, nil
}

func (c *ctx) cmdRotateToken(o opts) (int, error) {
	_, dataDir, err := project(o)
	if err != nil {
		return 0, err
	}
	_, st, err := daemonDiscover(dataDir)
	if err != nil {
		return 0, err
	}
	wasRunning := st != daemon.Stopped
	if wasRunning {
		if err := daemonStop(dataDir, readAdminToken(dataDir), 10*time.Second); err != nil {
			return 0, err
		}
	}
	tok := ids.AdminToken()
	if err := store.WriteFileAtomic(adminTokenPath(dataDir), []byte(tok+"\n"), 0o600); err != nil {
		return 0, err
	}
	if wasRunning {
		if _, _, err := c.ensureHub(o); err != nil {
			return 0, fmt.Errorf("token rotated but the hub did not restart: %w", err)
		}
	}
	fmt.Fprintf(c.stdout, "admin token: %s\n", tok)
	return 0, nil
}
