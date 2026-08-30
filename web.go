package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ui.html
var uiFiles embed.FS

// server holds the one-at-a-time job state behind the UI.
type server struct {
	token string
	mu    sync.Mutex
	jobs  map[string]*job
	next  int
	// runOne serializes job bodies. Output capture replaces os.Stdout for the
	// whole process, so overlapping jobs would corrupt each other's logs.
	runOne sync.Mutex
}

type job struct {
	ID     string
	Kind   string
	Done   bool
	Err    string
	Lines  []string
	Code   int
	Result any // structured payload, when the job produces one
	mu     sync.Mutex
}

func (j *job) setResult(v any) {
	j.mu.Lock()
	j.Result = v
	j.mu.Unlock()
}

// jobView is the lock-free shape sent to the browser.
type jobView struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Done   bool     `json:"done"`
	Err    string   `json:"err"`
	Lines  []string `json:"lines"`
	Code   int      `json:"code"`
	Result any      `json:"result,omitempty"`
}

func (j *job) snapshot() jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return jobView{
		ID: j.ID, Kind: j.Kind, Done: j.Done, Err: j.Err, Code: j.Code,
		Lines: append([]string{}, j.Lines...), Result: j.Result,
	}
}

// write appends output, honouring \r as "overwrite the current line" so the
// progress counters render as one updating line instead of hundreds.
func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, chunk := range strings.SplitAfter(string(p), "\n") {
		if chunk == "" {
			continue
		}
		text := strings.TrimSuffix(chunk, "\n")
		ended := strings.HasSuffix(chunk, "\n")
		if i := strings.LastIndexByte(text, '\r'); i >= 0 {
			text = text[i+1:]
			if len(j.Lines) > 0 {
				j.Lines[len(j.Lines)-1] = text // overwrite in place
			} else {
				j.Lines = append(j.Lines, text)
			}
			if ended {
				j.Lines = append(j.Lines, "")
			}
			continue
		}
		if len(j.Lines) == 0 {
			j.Lines = append(j.Lines, "")
		}
		j.Lines[len(j.Lines)-1] += text
		if ended {
			j.Lines = append(j.Lines, "")
		}
	}
	if len(j.Lines) > 4000 {
		j.Lines = j.Lines[len(j.Lines)-4000:]
	}
	return len(p), nil
}

// start runs fn with stdout/stderr captured into the job. Jobs are serialized by
// the caller, since the capture swaps the process-wide os.Stdout.
func (s *server) start(kind string, fn func(j *job)) *job {
	s.mu.Lock()
	s.next++
	j := &job{ID: fmt.Sprintf("j%d", s.next), Kind: kind, Lines: []string{}}
	s.jobs[j.ID] = j
	s.mu.Unlock()

	go func() {
		s.runOne.Lock()
		defer s.runOne.Unlock()

		oldOut, oldErr := os.Stdout, os.Stderr
		r, w, err := os.Pipe()
		if err != nil {
			j.mu.Lock()
			j.Err, j.Done = err.Error(), true
			j.mu.Unlock()
			return
		}
		os.Stdout, os.Stderr = w, w

		copied := make(chan struct{})
		go func() { io.Copy(j, r); close(copied) }()

		func() {
			defer func() {
				if rec := recover(); rec != nil {
					if fe, ok := rec.(fatalError); ok {
						fmt.Fprintf(w, "error: %s\n", fe.msg)
						j.mu.Lock()
						j.Err, j.Code = fe.msg, 2
						j.mu.Unlock()
						return
					}
					fmt.Fprintf(w, "unexpected failure: %v\n", rec)
					j.mu.Lock()
					j.Err, j.Code = fmt.Sprint(rec), 2
					j.mu.Unlock()
				}
			}()
			fn(j)
		}()

		w.Close()
		<-copied
		r.Close()
		os.Stdout, os.Stderr = oldOut, oldErr

		j.mu.Lock()
		j.Done = true
		j.mu.Unlock()
	}()
	return j
}

func cmdGUI(args guiArgs) int {
	// Double-clicking an app that is already running should show you the one you
	// have, not start a second invisible copy on a different port.
	if !args.noBrowser {
		if prev, ok := liveSession(); ok {
			fmt.Printf("wowbak is already running at:\n  %s\n", prev.URL)
			openBrowser(prev.URL)
			return 0
		}
	}

	// Launched from a macOS app bundle, hand the server to a detached child and
	// exit at once.
	//
	// macOS treats a .app as something it can activate, and expects it to answer.
	// A plain Go binary never registers with the window server, so an instance
	// left running makes the next launch fail with "the application is not
	// responding" - macOS is waiting for a reply that never comes. Exiting
	// immediately means there is never a running app to activate.
	if args.serve == false && !args.noBrowser && bundleFor(currentExe()) != "" {
		return launchDetached(args)
	}

	tok := make([]byte, 16)
	rand.Read(tok)
	s := &server{token: hex.EncodeToString(tok), jobs: map[string]*job{}}

	// Loopback only: this process can read and write your WoW folder, so it must
	// not be reachable from the network.
	ln, err := net.Listen("tcp", "127.0.0.1:"+fmt.Sprint(args.port))
	if err != nil {
		fatalf("cannot start the interface: %v", err)
	}
	url := fmt.Sprintf("http://%s/?t=%s", ln.Addr().String(), s.token)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.guard(s.handleUI))
	mux.HandleFunc("/api/state", s.guard(s.handleState))
	mux.HandleFunc("/api/backup", s.guard(s.handleBackup))
	mux.HandleFunc("/api/diff", s.guard(s.handleDiff))
	mux.HandleFunc("/api/restore", s.guard(s.handleRestore))
	mux.HandleFunc("/api/job", s.guard(s.handleJob))
	mux.HandleFunc("/api/token", s.guard(s.handleToken))
	mux.HandleFunc("/api/sources", s.guard(s.handleSources))
	mux.HandleFunc("/api/outdated", s.guard(s.handleOutdated))
	mux.HandleFunc("/api/update", s.guard(s.handleUpdate))
	mux.HandleFunc("/api/prune", s.guard(s.handlePrune))
	mux.HandleFunc("/api/delete", s.guard(s.handleDelete))
	mux.HandleFunc("/api/version", s.guard(s.handleVersion))
	mux.HandleFunc("/api/self-update", s.guard(s.handleSelfUpdate))
	mux.HandleFunc("/api/quit", s.guard(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
		go func() {
			time.Sleep(200 * time.Millisecond)
			clearSession()
			os.Exit(0)
		}()
	}))

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	writeSession(session{PID: os.Getpid(), Port: p, URL: url})
	defer clearSession()

	fmt.Println("wowbak interface running at:")
	fmt.Printf("  %s\n\n", url)
	fmt.Println("Leave this window open while you use it. Close it, or press Ctrl+C, to stop.")
	if !args.noBrowser {
		if err := openBrowser(url); err != nil {
			// Launched from the app bundle there is no console to print to, so
			// the address has to be put on screen or it is unreachable.
			showDialog("WowBackup",
				"WowBackup is running, but your browser could not be opened.\n\n"+
					"Open this address yourself:\n"+url)
		}
	}
	if err := http.Serve(ln, mux); err != nil {
		fatalf("interface stopped: %v", err)
	}
	return 0
}

// guard rejects requests without the session token, and requests whose Host is
// not loopback, which is what stops a web page you have open from driving this.
func (s *server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !strings.HasPrefix(origin, "http://127.0.0.1:") &&
				!strings.HasPrefix(origin, "http://localhost:") {
				http.Error(w, "bad origin", http.StatusForbidden)
				return
			}
		}
		got := r.URL.Query().Get("t")
		if got == "" {
			got = r.Header.Get("X-Wowbak-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "this link has expired - restart wowbak", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")

		// Handlers signal bad input with fatalf, which panics. Turn that into a
		// plain 400 the page can display; without this the server would drop the
		// connection and the browser would report an unhelpful network error.
		defer func() {
			if rec := recover(); rec != nil {
				fe, ok := rec.(fatalError)
				if !ok {
					panic(rec)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": fe.msg})
			}
		}()
		next(w, r)
	}
}

func (s *server) handleUI(w http.ResponseWriter, r *http.Request) {
	data, err := uiFiles.ReadFile("ui.html")
	if err != nil {
		http.Error(w, "ui missing", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

type flavorInfo struct {
	Name     string `json:"name"`
	Addons   int    `json:"addons"`
	HasWTF   bool   `json:"hasWtf"`
	Writable bool   `json:"writable"`
}

type archiveInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     string `json:"size"`
	Modified string `json:"modified"`
	Summary  string `json:"summary"`
	Snapshot bool   `json:"snapshot"`
}

type machineInfo struct {
	Name        string        `json:"name"`
	OS          string        `json:"os"`
	InstallPath string        `json:"installPath"`
	IsCurrent   bool          `json:"isCurrent"`
	Archives    []archiveInfo `json:"archives"`
}

type stateResponse struct {
	Version     string        `json:"version"`
	DevBuild    bool          `json:"devBuild"`
	Machine     string        `json:"machine"`
	OS          string        `json:"os"`
	InstallPath string        `json:"installPath"`
	InstallOK   bool          `json:"installOk"`
	InstallErr  string        `json:"installErr"`
	ConfigPath  string        `json:"configPath"`
	BackupRoot  string        `json:"backupRoot"`
	Flavors     []flavorInfo  `json:"flavors"`
	Machines    []machineInfo `json:"machines"`
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	cfg := loadConfig()
	me := machineID()
	resp := stateResponse{
		Version:    versionString(),
		DevBuild:   isDevBuild(),
		Machine:    me,
		OS:         osLabel(),
		ConfigPath: cfg.pathOrNone(),
		BackupRoot: cfg.backupRoot(),
	}

	// resolveInstall panics via fatalf when nothing is found; that is a state to
	// display, not a crash.
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				if fe, ok := rec.(fatalError); ok {
					resp.InstallErr = fe.msg
					return
				}
				panic(rec)
			}
		}()
		resp.InstallPath = resolveInstall("")
		resp.InstallOK = true
	}()

	if resp.InstallOK {
		for _, f := range presentFlavors(resp.InstallPath) {
			addonDir := filepath.Join(resp.InstallPath, f, "Interface", "AddOns")
			n := 0
			if entries, err := os.ReadDir(addonDir); err == nil {
				n = len(entries)
			}
			resp.Flavors = append(resp.Flavors, flavorInfo{
				Name:     f,
				Addons:   n,
				HasWTF:   isDir(filepath.Join(resp.InstallPath, f, "WTF")),
				Writable: checkWritable(filepath.Join(resp.InstallPath, f)) == nil,
			})
		}
	}

	groups := collectArchives(cfg.backupRoot())
	names := map[string]bool{me: true}
	for n := range groups {
		names[n] = true
	}
	for n := range cfg.Machines {
		names[n] = true
	}
	for name := range names {
		mi := machineInfo{
			Name:        name,
			InstallPath: cfg.Machines[name],
			IsCurrent:   name == me,
		}
		for _, row := range groups[name] {
			if mi.OS == "" {
				mi.OS = row.os
			}
			mi.Archives = append(mi.Archives, archiveInfo{
				Name:     row.name,
				Path:     row.path,
				Size:     human(row.size),
				Modified: row.mod.Format("2006-01-02 15:04"),
				Summary:  row.summary,
				Snapshot: isSnapshot(row.name),
			})
		}
		if mi.IsCurrent && mi.OS == "" {
			mi.OS = osLabel()
		}
		if mi.IsCurrent && mi.InstallPath == "" {
			mi.InstallPath = resp.InstallPath
		}
		resp.Machines = append(resp.Machines, mi)
	}
	writeJSON(w, resp)
}

func decode(r *http.Request, v any) {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		fatalf("bad request: %v", err)
	}
}

func (s *server) handleBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Flavors        []string `json:"flavors"`
		IncludeConfig  bool     `json:"includeConfig"`
		FollowSymlinks bool     `json:"followSymlinks"`
	}
	decode(r, &req)
	j := s.start("backup", func(*job) {
		cmdBackup(backupArgs{
			flavor:         req.Flavors,
			includeConfig:  req.IncludeConfig,
			followSymlinks: req.FollowSymlinks,
		})
	})
	writeJSON(w, map[string]string{"id": j.ID})
}

func (s *server) handleDiff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Archive string `json:"archive"`
		Files   bool   `json:"files"`
	}
	decode(r, &req)
	j := s.start("diff", func(*job) {
		cmdDiff(diffArgs{archive: req.Archive, files: req.Files})
	})
	writeJSON(w, map[string]string{"id": j.ID})
}

func (s *server) handleRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Archive    string `json:"archive"`
		DryRun     bool   `json:"dryRun"`
		Mirror     bool   `json:"mirror"`
		Clean      bool   `json:"clean"`
		AddonsOnly bool   `json:"addonsOnly"`
		WTFOnly    bool   `json:"wtfOnly"`
	}
	decode(r, &req)
	j := s.start("restore", func(*job) {
		cmdRestore(restoreArgs{
			archive:       req.Archive,
			dryRun:        req.DryRun,
			force:         !req.DryRun, // the UI confirms before calling this
			mirror:        req.Mirror,
			clean:         req.Clean,
			addonsOnly:    req.AddonsOnly,
			wtfOnly:       req.WTFOnly,
			createMissing: true,
		})
	})
	writeJSON(w, map[string]string{"id": j.ID})
}

type tokenStatus struct {
	Set      bool   `json:"set"`
	Masked   string `json:"masked"`
	Source   string `json:"source"`
	FromEnv  bool   `json:"fromEnv"`
	Path     string `json:"path"`
	OnDevice bool   `json:"onDevice"` // a token file exists on disk
}

// handleToken reports and updates the GitHub token. It never sends the token
// itself back to the browser, only a masked form.
func (s *server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Token string `json:"token"`
			Clear bool   `json:"clear"`
		}
		decode(r, &req)
		if req.Clear {
			if err := os.Remove(tokenPath()); err != nil && !os.IsNotExist(err) {
				fatalf("could not remove the token file: %v", err)
			}
		} else {
			if strings.TrimSpace(req.Token) == "" {
				fatalf("no token given")
			}
			if _, err := saveGitHubToken(req.Token); err != nil {
				fatalf("could not save the token: %v", err)
			}
		}
	}

	cfg := loadConfig()
	tok, src := cfg.githubToken()
	st := tokenStatus{
		Set:      tok != "",
		Source:   src,
		Path:     tokenPath(),
		OnDevice: fileExists(tokenPath()),
	}
	if tok != "" {
		st.Masked = maskToken(tok)
		st.FromEnv = strings.HasPrefix(src, "environment")
	}
	writeJSON(w, st)
}

type addonRow struct {
	Flavor    string `json:"flavor"`
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
	Source    string `json:"source"`
	State     string `json:"state"` // outdated | current | untracked | error
	Reason    string `json:"reason"`
	Folders   int    `json:"folders"`
	URL       string `json:"url"`
	Build     string `json:"build"` // release flavor the build was matched on
}

type outdatedResult struct {
	Rows     []addonRow `json:"rows"`
	Outdated int        `json:"outdated"`
	Checked  int        `json:"checked"`
	Rate     string     `json:"rate"`
}

func (s *server) handleOutdated(w http.ResponseWriter, r *http.Request) {
	j := s.start("outdated", func(j *job) {
		fmt.Println("reading installed addons...")
		cfg := loadConfig()
		install := resolveInstall("")
		flavors := resolveFlavors(install, nil)

		done := 0
		checks, gh := runCheck(cfg, install, flavors, nil, func(name string) {
			done++
			fmt.Printf("checking %s (%d)\r", name, done)
		})
		fmt.Printf("checked %d addon(s)          \n", done)

		out := outdatedResult{Rate: gh.rateNote()}
		for _, fc := range checks {
			for _, res := range fc.Results {
				row := addonRow{
					Flavor: fc.Flavor, Name: res.Pkg.Name,
					Installed: res.Pkg.Version, Folders: len(res.Pkg.Folders),
				}
				switch {
				case res.Untracked:
					row.State = "untracked"
				case res.Reason != "":
					row.State, row.Reason = "error", res.Reason
				default:
					out.Checked++
					row.Latest, row.Source = res.Latest, res.Source.String()
					row.URL, row.Build = res.Release.HTMLURL, res.Flavor
					if res.Outdated {
						row.State = "outdated"
						out.Outdated++
					} else {
						row.State = "current"
					}
				}
				out.Rows = append(out.Rows, row)
			}
		}
		j.setResult(out)
	})
	writeJSON(w, map[string]string{"id": j.ID})
}

func (s *server) handleSources(w http.ResponseWriter, r *http.Request) {
	j := s.start("sources", func(*job) {
		fmt.Println("reading installed addons...")
		cmdSources(sourcesArgs{discover: true})
	})
	writeJSON(w, map[string]string{"id": j.ID})
}

func (s *server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names  []string `json:"names"`
		All    bool     `json:"all"`
		DryRun bool     `json:"dryRun"`
	}
	decode(r, &req)
	if !req.All && len(req.Names) == 0 {
		fatalf("nothing selected to update")
	}
	j := s.start("update", func(*job) {
		cmdUpdate(updateArgs{names: req.Names, all: req.All, dryRun: req.DryRun})
	})
	writeJSON(w, map[string]string{"id": j.ID})
}

func (s *server) handlePrune(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Machine string `json:"machine"`
		Force   bool   `json:"force"`
		Backups bool   `json:"backups"`
	}
	decode(r, &req)
	if req.Machine == "" {
		fatalf("no machine given")
	}
	j := s.start("prune", func(*job) {
		cmdPrune(pruneArgs{machine: req.Machine, force: req.Force, backups: req.Backups})
	})
	writeJSON(w, map[string]string{"id": j.ID})
}

type versionResponse struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	DevBuild  bool   `json:"devBuild"`
	URL       string `json:"url"`
	Error     string `json:"error"`
	CanUpdate bool   `json:"canUpdate"` // a download exists for this platform
	Siblings  int    `json:"siblings"`  // other platforms' binaries in this folder
}

// handleVersion checks the repository for a newer release. One request, so it
// answers directly rather than going through the job machinery.
// handleDelete removes one archive. deleteArchive validates that the path is
// inside the backup folder, since it arrives from the page.
func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	decode(r, &req)
	if req.Path == "" {
		fatalf("no archive given")
	}
	if err := deleteArchive(loadConfig(), req.Path); err != nil {
		fatalf("could not delete it: %v", err)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *server) handleVersion(w http.ResponseWriter, r *http.Request) {
	cfg := loadConfig()
	tok, _ := cfg.githubToken()
	info, err := checkSelfUpdate(newGHClient(tok))

	out := versionResponse{Current: versionString(), DevBuild: isDevBuild()}
	if err != nil {
		out.Error = err.Error()
		writeJSON(w, out)
		return
	}
	out.Latest, out.URL, out.Available = info.Latest, info.URL, info.Available
	out.CanUpdate = info.Asset.URL != ""
	if self, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
		out.Siblings = len(siblingTargets(self)) - 1
	}
	writeJSON(w, out)
}

func (s *server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		All bool `json:"all"`
	}
	decode(r, &req)
	j := s.start("self-update", func(*job) {
		cmdSelfUpdate(selfUpdateArgs{all: req.All})
	})
	writeJSON(w, map[string]string{"id": j.ID})
}

func (s *server) handleJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j := s.jobs[r.URL.Query().Get("id")]
	s.mu.Unlock()
	if j == nil {
		http.Error(w, "no such job", 404)
		return
	}
	snap := j.snapshot()
	writeJSON(w, snap)
}

// currentExe resolves the running binary, following symlinks.
func currentExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// launchDetached starts the server in a child process, waits for it to come up,
// opens the browser and returns so the bundle's own process can exit.
func launchDetached(args guiArgs) int {
	exe := currentExe()
	if exe == "" {
		fatalf("cannot locate the running program")
	}

	logPath := filepath.Join(os.TempDir(), "wowbak-gui.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		logFile = nil
	}

	// --no-browser matters: the launcher opens the page once the child is up.
	// Without it the child opens one too and you get two tabs.
	cmd := exec.Command(exe, "gui", "--serve", "--no-browser")
	if logFile != nil {
		cmd.Stdout, cmd.Stderr = logFile, logFile
	}
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		showDialog("WowBackup", "WowBackup could not start.\n\n"+err.Error())
		fatalf("could not start the interface: %v", err)
	}
	if logFile != nil {
		logFile.Close()
	}

	// Wait for the child to record its address.
	for i := 0; i < 100; i++ {
		if s, ok := liveSession(); ok {
			openBrowser(s.URL)
			fmt.Printf("wowbak interface running at:\n  %s\n", s.URL)
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}

	showDialog("WowBackup",
		"WowBackup started but did not become ready.\n\nDetails are in:\n"+logPath)
	return 1
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "could not open a browser automatically: %v\n", err)
		fmt.Fprintln(os.Stderr, "Copy the address above into your browser instead.")
		return err
	}
	return nil
}

// --------------------------------------------------------------- one instance

// session records a running interface so a second launch can join it rather
// than starting another invisible server. It lives in the temp directory, not
// beside the binary: it is per-machine state, it holds the session token, and a
// USB stick is often a filesystem with no usable permissions.
type session struct {
	PID  int    `json:"pid"`
	Port int    `json:"port"`
	URL  string `json:"url"`
}

func sessionPath() string {
	h := sha256.Sum256([]byte(exeDir()))
	return filepath.Join(os.TempDir(), fmt.Sprintf("wowbak-session-%x.json", h[:8]))
}

// liveSession returns the running interface for this install, if there is one.
// A recorded session whose port no longer answers is stale and ignored.
func liveSession() (*session, bool) {
	data, err := os.ReadFile(sessionPath())
	if err != nil {
		return nil, false
	}
	var s session
	if json.Unmarshal(data, &s) != nil || s.Port == 0 || s.URL == "" {
		return nil, false
	}
	conn, err := net.DialTimeout("tcp",
		fmt.Sprintf("127.0.0.1:%d", s.Port), 700*time.Millisecond)
	if err != nil {
		return nil, false // nothing listening; the previous run is gone
	}
	conn.Close()
	return &s, true
}

func writeSession(s session) {
	data, _ := json.Marshal(s)
	os.WriteFile(sessionPath(), data, 0o600)
}

func clearSession() { os.Remove(sessionPath()) }

// showDialog puts a message on screen. When launched from an app bundle there
// is no console, so without this a failure to open the browser is completely
// silent - the app appears to do nothing at all.
func showDialog(title, message string) {
	if runtime.GOOS != "darwin" {
		return
	}
	script := fmt.Sprintf(
		`display dialog %s with title %s buttons {"OK"} default button "OK"`,
		osaQuote(message), osaQuote(title))
	exec.Command("/usr/bin/osascript", "-e", script).Run()
}

func osaQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
