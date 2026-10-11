package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
)

// --- nick -------------------------------------------------------------------------------

type nameDialog struct {
	in    textInput
	first bool
	err   string
	next  dialog // okno po zamknieciu (np. dolaczanie z `abcollab join`)
}

func newNameDialog(name string, first bool) *nameDialog {
	return &nameDialog{in: textInput{value: name}, first: first}
}

func (d *nameDialog) form(a *App) form {
	f := form{title: "Your nickname", input: &d.in, err: d.err, width: 56,
		hint: "enter save · esc cancel"}
	if d.first {
		f.title = "Welcome to ableton collab"
		f.body = []string{"Pick a nickname - others in the session will see it."}
	}
	return f
}

func (d *nameDialog) update(a *App, msg tea.Msg) (dialog, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	switch k.String() {
	case "esc":
		return d.next, nil
	case "enter":
		name := strings.TrimSpace(d.in.value)
		if name == "" {
			d.err = "The nickname can't be empty"
			return d, nil
		}
		if err := a.be.Rename(name); err != nil {
			d.err = err.Error()
			return d, nil
		}
		a.me.Name = name
		if a.ses != nil {
			a.ses.p.Me.Name = name
		}
		a.say(lOK, "Nickname set to "+name)
		return d.next, nil
	}
	d.in.key(k)
	return d, nil
}

// --- potwierdzenie ------------------------------------------------------------------------

type confirmDialog struct {
	title string
	body  []string
	cur   int
	opts  []option
	do    func(a *App) tea.Cmd
}

func newConfirmDialog(title string, body []string, yes string, danger bool, do func(a *App) tea.Cmd) *confirmDialog {
	return &confirmDialog{title: title, body: body, cur: 1, do: do,
		opts: []option{{label: yes, danger: danger}, {label: "Cancel"}}}
}

func (d *confirmDialog) form(a *App) form {
	return form{title: d.title, body: d.body, options: d.opts, cur: d.cur, width: 60}
}

func (d *confirmDialog) update(a *App, msg tea.Msg) (dialog, tea.Cmd) {
	if isEsc(msg) {
		return nil, nil
	}
	switch optionKeys(&d.cur, len(d.opts), msg) {
	case 0:
		return nil, d.do(a)
	case 1:
		return nil, nil
	}
	return d, nil
}

// --- zaproszenie --------------------------------------------------------------------------

type inviteDialog struct {
	code   string
	copied bool
	cur    int
}

func newInviteDialog(a *App) *inviteDialog {
	code := a.ses.net.Invite(a.me.Name)
	return &inviteDialog{code: code, copied: clipboard(code) == nil}
}

func (d *inviteDialog) form(a *App) form {
	note := "Send this code to the person you're inviting (e.g. on Discord):"
	if d.copied {
		note = "The code is in your clipboard - paste it to the person you're inviting:"
	}
	body := []string{note, ""}
	for _, l := range wrap(d.code, 66) {
		body = append(body, sCode.Render(l))
	}
	body = append(body, "",
		"They press Join in abcollab and paste it. Their request shows up in PEOPLE - accept it if the device code matches the one they see.")
	return form{title: "Invite to " + sessionName(a.ses.p), body: body, width: 74,
		options: []option{{label: "Copy again"}, {label: "Close"}}, cur: d.cur}
}

func (d *inviteDialog) update(a *App, msg tea.Msg) (dialog, tea.Cmd) {
	if isEsc(msg) {
		return nil, nil
	}
	switch optionKeys(&d.cur, 2, msg) {
	case 0:
		d.copied = clipboard(d.code) == nil
	case 1:
		return nil, nil
	}
	return d, nil
}

// --- ustawienia ---------------------------------------------------------------------------

type settingsDialog struct {
	cur      int
	sessions []SessionRef
	err      string
}

func newSettingsDialog(a *App) *settingsDialog {
	d := &settingsDialog{}
	if ss, err := a.be.Sessions(); err == nil {
		d.sessions = ss
	} else {
		d.err = err.Error()
	}
	return d
}

const settingsFixed = 3 // nick, auto sync, auto-save

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func (d *settingsDialog) opts(a *App) []option {
	o := []option{
		{label: "Nickname: " + a.me.Name, detail: "change how others see you"},
		{label: "Automatic sync: " + onOff(a.settings.AutoSync), detail: "when Live saves the set, share it right away and get others' changes on their own"},
		{label: "Auto-save in Live: " + onOff(a.settings.AutoSave), detail: "press Ctrl+S in Live for you a few seconds after you change something (only while Live is in front; on macOS needs Accessibility permission)"},
	}
	for _, s := range d.sessions {
		label := "Open session " + s.Name
		if a.ses != nil && samePath(a.ses.p.Dir, s.Dir) {
			label += "  (open)"
		}
		o = append(o, option{label: label, detail: s.Dir})
	}
	return append(o, option{label: "Close"})
}

func (d *settingsDialog) form(a *App) form {
	f := form{title: "Settings", options: d.opts(a), cur: d.cur, err: d.err, width: 70}
	if len(d.sessions) == 0 {
		f.body = []string{"No sessions yet - Host or Join one."}
	}
	return f
}

func (d *settingsDialog) update(a *App, msg tea.Msg) (dialog, tea.Cmd) {
	if isEsc(msg) {
		return nil, nil
	}
	n := len(d.opts(a))
	i := optionKeys(&d.cur, n, msg)
	switch {
	case i == 0:
		return newNameDialog(a.me.Name, false), nil
	case i == 1 || i == 2:
		s := a.settings
		if i == 1 {
			s.AutoSync = !s.AutoSync
		} else {
			s.AutoSave = !s.AutoSave
		}
		if err := a.be.SetSettings(s); err != nil {
			d.err = err.Error()
			return d, nil
		}
		a.settings = s
		return d, nil
	case i == n-1:
		return nil, nil
	case i >= settingsFixed:
		s := d.sessions[i-settingsFixed]
		return nil, a.openCmd(s.Dir, false)
	}
	return d, nil
}

// --- host ---------------------------------------------------------------------------------

type hostStep int

const (
	hPath hostStep = iota
	hBig
	hPick
	hWaitSet
)

type hostDialog struct {
	step    hostStep
	path    *pathInput
	info    FolderInfo
	cur     int
	err     string
	busy    bool
	polling bool
}

type inspectMsg struct {
	info FolderInfo
	err  error
}

func newHostDialog(a *App) *hostDialog {
	return &hostDialog{path: newPathInput(a.opts.BrowseFrom)}
}

func (d *hostDialog) form(a *App) form {
	switch d.step {
	case hBig:
		return form{title: "Big folder", width: 64, cur: d.cur,
			body:    []string{dirFit(d.info.Dir, 58), humanCount(d.info), "", "Everything in this folder is sent to everyone in the session. Sure?"},
			options: []option{{label: "Yes, share it"}, {label: "Pick another folder"}}}
	case hPick:
		var o []option
		for _, s := range d.info.Sets {
			o = append(o, option{label: s})
		}
		return form{title: "Which set is this session's project?", width: 70, options: o, cur: d.cur,
			body: []string{"The other sets stay in the folder but aren't merged."}, hint: "enter pick · esc back"}
	case hWaitSet:
		return form{title: "Save your project from Live into this folder", width: 74,
			body: []string{sBold.Render(dirFit(d.info.Dir, 64)), "",
				"In Live: File > Save Live Set As... and pick this folder.",
				"Live may create a \"<name> Project\" subfolder - that's fine.", "",
				sWarnText.Render("waiting for an .als file...")},
			hint: "esc back"}
	}
	return form{title: "Host a session", width: 74, path: d.path, err: d.err,
		body: []string{"Type the session folder. Everything in it (set, samples, recordings...) is shared with everyone in the session."},
		hint: "type to filter · tab/↑↓ browse · enter use this folder · esc cancel"}
}

func (d *hostDialog) inspect(dir string) tea.Cmd {
	d.busy = true
	return func() tea.Msg {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return inspectMsg{err: err}
		}
		info, err := InspectFolder(dir)
		return inspectMsg{info, err}
	}
}

func (d *hostDialog) update(a *App, msg tea.Msg) (dialog, tea.Cmd) {
	switch m := msg.(type) {
	case inspectMsg:
		d.busy, d.polling = false, false
		if m.err != nil {
			d.step, d.err = hPath, m.err.Error()
			return d, nil
		}
		prev := d.step
		d.info = m.info
		if m.info.Project {
			return nil, a.openCmd(m.info.Dir, false)
		}
		if prev == hPath && m.info.Big() {
			d.step, d.cur = hBig, 1
			return d, nil
		}
		return d.afterBig(a)
	case tickMsg:
		if d.step == hWaitSet && !d.polling {
			d.polling = true
			info := d.info
			return d, func() tea.Msg {
				i, err := InspectFolder(info.Dir)
				return inspectMsg{i, err}
			}
		}
		return d, nil
	}
	if isEsc(msg) {
		if d.step == hPath {
			return nil, nil
		}
		d.step, d.err = hPath, ""
		return d, nil
	}
	switch d.step {
	case hPath:
		k, ok := msg.(tea.KeyMsg)
		if !ok {
			if c, ok := msg.(chooseMsg); ok {
				d.path.complete(c.i)
			}
			return d, nil
		}
		if submit, _ := d.path.key(k); submit && !d.busy {
			d.err = ""
			if d.path.Path() == "" {
				return d, nil
			}
			return d, d.inspect(d.path.Path())
		}
	case hBig:
		switch optionKeys(&d.cur, 2, msg) {
		case 0:
			return d.afterBig(a)
		case 1:
			d.step = hPath
		}
	case hPick:
		if i := optionKeys(&d.cur, len(d.info.Sets), msg); i >= 0 {
			return d.host(a, d.info.Sets[i])
		}
	}
	return d, nil
}

// afterBig: folder zaakceptowany - set gotowy, wybor albo czekanie na Live.
func (d *hostDialog) afterBig(a *App) (dialog, tea.Cmd) {
	var ready []string
	for _, s := range d.info.Sets {
		if setReady(d.info.Dir, s) {
			ready = append(ready, s)
		}
	}
	switch {
	case len(ready) == 1:
		return d.host(a, ready[0])
	case len(ready) > 1:
		d.info.Sets, d.step, d.cur = ready, hPick, 0
		return d, nil
	}
	d.step = hWaitSet
	return d, nil
}

func (d *hostDialog) host(a *App, set string) (dialog, tea.Cmd) {
	a.sid++
	sid, be, dir := a.sid, a.be, d.info.Dir
	return nil, func() tea.Msg {
		p, net, err := be.Host(dir, set)
		return openedMsg{sid: sid, p: p, net: net, err: err, invite: true}
	}
}

// --- join ---------------------------------------------------------------------------------

type joinStep int

const (
	jCode joinStep = iota
	jWhere
	jPath
	jBig
	jAction
	jPick
	jDelete
	jWait
)

type joinDialog struct {
	step    joinStep
	code    textInput
	inv     syncthing.Invite
	where   int // 0 nowy folder, 1 istniejacy
	path    *pathInput
	info    FolderInfo
	cur     int
	merge   bool
	mySet   string // przy scalaniu: moj set (wzgledem folderu)
	err     string
	busy    bool
	net     Network
	dir     string
	wait    string
	polling bool
	res     map[string]als.Resolution
}

type joinedMsg struct {
	net Network
	err error
}

type joinPollMsg struct {
	text  string
	ready bool
	pull  *store.PullResult // scalanie: konflikty do wyboru
	err   error
}

type deletedMsg struct{ err error }

func newJoinDialog(a *App) *joinDialog { return &joinDialog{} }

// newJoinDialogWithCode: kod z linii polecen - poprawny od razu przechodzi dalej.
func newJoinDialogWithCode(code string) *joinDialog {
	d := &joinDialog{code: textInput{value: code}}
	if inv, err := syncthing.ParseInvite(code); err == nil {
		d.inv, d.step = inv, jWhere
	} else {
		d.err = err.Error()
	}
	return d
}

func (d *joinDialog) form(a *App) form {
	switch d.step {
	case jWhere:
		return form{title: "Join " + d.inv.Name + " (hosted by " + d.inv.Host + ")", width: 74, cur: d.cur,
			options: []option{
				{label: "New folder", detail: "Creates a new folder and downloads a copy of the host's session into it."},
				{label: "Existing folder", detail: "Use a folder you already have - e.g. your own copy of this project."},
			}, hint: "enter pick · esc back"}
	case jPath:
		title := "Where should the new folder be?"
		body := "Type the path of the new session folder. It must not exist yet, or be empty."
		if d.where == 1 {
			title, body = "Pick your existing folder", "Type the folder with your copy of the project."
		}
		return form{title: title, width: 74, path: d.path, err: d.err, body: []string{body},
			hint: "type to filter · tab/↑↓ browse · enter use this folder · esc back"}
	case jBig:
		return form{title: "Big folder", width: 64, cur: d.cur,
			body:    []string{dirFit(d.info.Dir, 58), humanCount(d.info), "", "Everything in this folder is sent to everyone in the session. Sure?"},
			options: []option{{label: "Yes, use it"}, {label: "Pick another folder"}}}
	case jAction:
		var o []option
		if len(d.info.Sets) > 0 {
			o = append(o, option{label: "Merge files",
				detail: "Combines your set with the host's. Parts only one of you has are kept; where you both changed the same thing, you choose. Your other files are shared too."})
		}
		o = append(o, option{label: "Replace with the host's files", danger: true,
			detail: "For an empty folder or one with junk only: DELETES everything in this folder, then downloads the host's session."})
		return form{title: "This folder already has files", width: 76, cur: d.cur, options: o,
			body: []string{dirFit(d.info.Dir, 44) + "  ·  " + humanCount(d.info)}, hint: "enter pick · esc back"}
	case jPick:
		var o []option
		for _, s := range d.info.Sets {
			o = append(o, option{label: s})
		}
		return form{title: "Which set is your copy of " + d.inv.Name + "?", width: 70, options: o, cur: d.cur,
			hint: "enter pick · esc back"}
	case jDelete:
		return form{title: "Delete everything in this folder?", width: 70, cur: d.cur,
			body: []string{sBold.Render(dirFit(d.info.Dir, 64)), humanCount(d.info), "",
				"All of it will be deleted and replaced with the host's session. This can't be undone."},
			options: []option{{label: "Delete and join", danger: true}, {label: "Back"}}}
	case jWait:
		w := d.wait
		if w == "" {
			w = "Connecting..."
		}
		return form{title: "Joining " + d.inv.Name, width: 74, err: d.err,
			body: []string{
				"Your device code: " + sCode.Render(syncthing.ShortID(a.be.DeviceID())),
				d.inv.Host + " sees it next to your request - tell them, so they know it's you.", "",
				sWarnText.Render("" + w),
				sDim.Render("folder: " + dirFit(d.dir, 60))},
			hint: "esc hide (the request stays - reopen the session from Settings)"}
	}
	return form{title: "Join a session", width: 74, input: &d.code, err: d.err,
		body: []string{"Paste the invite code you got from the host (it starts with ABC1)."},
		hint: "enter next · ctrl+u clear · esc cancel"}
}

func (d *joinDialog) update(a *App, msg tea.Msg) (dialog, tea.Cmd) {
	switch m := msg.(type) {
	case inspectMsg:
		d.busy = false
		if m.err != nil {
			d.err = m.err.Error()
			return d, nil
		}
		d.info = m.info
		if m.info.Big() {
			d.step, d.cur = jBig, 1
			return d, nil
		}
		return d.afterBig(a)
	case deletedMsg:
		d.busy = false
		if m.err != nil {
			d.step, d.err = jPath, m.err.Error()
			return d, nil
		}
		d.merge = false
		return d, d.join(a, false)
	case joinedMsg:
		d.busy = false
		if m.err != nil {
			d.step, d.err = jPath, m.err.Error()
			return d, nil
		}
		d.net, d.step, d.err = m.net, jWait, ""
		return d, nil
	case tickMsg:
		if d.step == jWait && !d.polling && d.net != nil {
			d.polling = true
			return d, d.poll(a)
		}
		return d, nil
	case joinPollMsg:
		d.polling = false
		if m.err != nil {
			d.err = m.err.Error()
			return d, nil
		}
		d.wait, d.err = m.text, ""
		if m.pull != nil && len(m.pull.Conflicts) > 0 {
			return newConflictDialog(a, m.pull, newerDefaults(m.pull), func(a *App, res map[string]als.Resolution) (dialog, tea.Cmd) {
				d.res, d.wait = res, "Merging..."
				d.polling = true
				return d, d.poll(a)
			}), nil
		}
		if m.ready {
			return nil, a.openCmd(d.dir, false)
		}
		return d, nil
	}
	if isEsc(msg) {
		switch d.step {
		case jCode, jWait:
			return nil, nil
		case jWhere:
			d.step = jCode
		case jPath:
			d.step, d.cur = jWhere, d.where
		default:
			d.step = jPath
		}
		d.err = ""
		return d, nil
	}
	switch d.step {
	case jCode:
		if k, ok := msg.(tea.KeyMsg); ok {
			if k.String() == "enter" {
				inv, err := syncthing.ParseInvite(d.code.value)
				if err != nil {
					d.err = err.Error()
					return d, nil
				}
				d.inv, d.err, d.step, d.cur = inv, "", jWhere, 0
				return d, nil
			}
			d.code.key(k)
		}
	case jWhere:
		if i := optionKeys(&d.cur, 2, msg); i >= 0 {
			d.where, d.step, d.err = i, jPath, ""
			if i == 0 {
				d.path = newPathInput("")
				d.path.value = filepath.Join(a.opts.BaseDir, d.inv.Name)
				d.path.refresh()
			} else {
				d.path = newPathInput(a.opts.BrowseFrom)
			}
		}
	case jPath:
		if c, ok := msg.(chooseMsg); ok {
			d.path.complete(c.i)
			return d, nil
		}
		k, ok := msg.(tea.KeyMsg)
		if !ok || d.busy {
			return d, nil
		}
		if submit, _ := d.path.key(k); submit {
			return d.submitPath(a)
		}
	case jBig:
		switch optionKeys(&d.cur, 2, msg) {
		case 0:
			return d.afterBig(a)
		case 1:
			d.step = jPath
		}
	case jAction:
		n := 1
		if len(d.info.Sets) > 0 {
			n = 2
		}
		i := optionKeys(&d.cur, n, msg)
		if n == 1 && i == 0 {
			i = 1
		}
		switch i {
		case 0:
			d.merge = true
			if len(d.info.Sets) == 1 {
				d.mySet = d.info.Sets[0]
				return d, d.join(a, true)
			}
			d.step, d.cur = jPick, 0
		case 1:
			d.step, d.cur = jDelete, 1
		}
	case jPick:
		if i := optionKeys(&d.cur, len(d.info.Sets), msg); i >= 0 {
			d.mySet = d.info.Sets[i]
			return d, d.join(a, true)
		}
	case jDelete:
		switch optionKeys(&d.cur, 2, msg) {
		case 0:
			d.busy = true
			dir := d.info.Dir
			return d, func() tea.Msg { return deletedMsg{clearFolder(dir)} }
		case 1:
			d.step, d.cur = jAction, 0
		}
	}
	return d, nil
}

func (d *joinDialog) submitPath(a *App) (dialog, tea.Cmd) {
	dir := d.path.Path()
	if dir == "" {
		return d, nil
	}
	d.err, d.dir = "", dir
	if d.where == 0 {
		if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
			d.err = "This folder isn't empty - pick another path, or go back and choose Existing folder."
			return d, nil
		}
		if why := forbidden(dir); why != "" {
			d.err = "You can't use this folder: " + why
			return d, nil
		}
		d.merge = false
		return d, d.join(a, false)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		d.err = "There's no such folder."
		return d, nil
	}
	d.busy = true
	return d, func() tea.Msg {
		info, err := InspectFolder(dir)
		return inspectMsg{info, err}
	}
}

func (d *joinDialog) afterBig(a *App) (dialog, tea.Cmd) {
	if d.info.Files == 0 {
		d.merge = false
		return d, d.join(a, false)
	}
	d.step, d.cur = jAction, 0
	return d, nil
}

func (d *joinDialog) join(a *App, existing bool) tea.Cmd {
	d.busy, d.dir = true, d.info.Dir
	if d.dir == "" {
		d.dir = d.path.Path()
	}
	be, inv, dir := a.be, d.inv, d.dir
	return func() tea.Msg {
		net, err := be.Join(inv, dir, existing)
		return joinedMsg{net, err}
	}
}

// poll: czy host nas przyjal i czy sesja doszla; potem pobranie albo scalenie.
func (d *joinDialog) poll(a *App) tea.Cmd {
	net, dir, me, host := d.net, d.dir, a.me, d.inv.Host
	merge, mySet, res := d.merge, d.mySet, d.res
	return func() tea.Msg {
		connected := false
		if l, err := net.Lobby(); err == nil {
			for _, m := range l.Members {
				connected = connected || m.Connected
			}
		}
		p, err := store.Open(dir, me)
		if errors.Is(err, store.ErrNotProject) {
			if connected {
				return joinPollMsg{text: "Connected to " + host + " - downloading the session..."}
			}
			return joinPollMsg{text: "Waiting for " + host + " to accept your request (they need abcollab open)."}
		}
		if err != nil {
			return joinPollMsg{err: err}
		}
		if p.Head() != "" {
			return joinPollMsg{ready: true}
		}
		if merge {
			r, err := p.Adopt(filepath.Join(dir, filepath.FromSlash(mySet)), res)
			if err != nil {
				return joinPollMsg{err: err}
			}
			if len(r.Incomplete) > 0 {
				return joinPollMsg{text: "Downloading the session..."}
			}
			if len(r.Conflicts) > 0 {
				return joinPollMsg{pull: r, text: "Choose what to keep"}
			}
			return joinPollMsg{ready: true}
		}
		r, err := p.Pull(nil)
		if err != nil {
			return joinPollMsg{err: err}
		}
		if p.Head() == "" || len(r.Incomplete) > 0 {
			return joinPollMsg{text: "Downloading the session..."}
		}
		return joinPollMsg{ready: true}
	}
}

// clearFolder usuwa zawartosc folderu (opcja "Replace with the host's files").
func clearFolder(dir string) error {
	if why := forbidden(dir); why != "" {
		return errors.New("you can't clear this folder: " + why)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// newerDefaults: przy scalaniu z wlasna kopia domyslnie zaznaczamy strone,
// ktora zapisala set pozniej.
func newerDefaults(r *store.PullResult) map[string]als.Resolution {
	pick := als.Theirs
	if r.OursTime.After(r.TheirsTime) {
		pick = als.Ours
	}
	out := map[string]als.Resolution{}
	for _, c := range r.Conflicts {
		out[c.ID] = pick
	}
	return out
}

// --- konflikty ----------------------------------------------------------------------------

type conflictDialog struct {
	r      *store.PullResult
	cur    int // ktory konflikt
	opt    int
	choice map[string]als.Resolution
	newer  map[string]als.Resolution
	done   func(a *App, res map[string]als.Resolution) (dialog, tea.Cmd)
}

// newConflictDialog: wybor moje/ich/oba dla kazdego konfliktu; newer -
// domyslne zaznaczenie (nowsza strona) albo nil.
func newConflictDialog(a *App, r *store.PullResult, newer map[string]als.Resolution, done func(a *App, res map[string]als.Resolution) (dialog, tea.Cmd)) *conflictDialog {
	d := &conflictDialog{r: r, choice: map[string]als.Resolution{}, newer: newer, done: done}
	d.reset()
	return d
}

func (d *conflictDialog) c() als.Conflict { return d.r.Conflicts[d.cur] }

func (d *conflictDialog) reset() {
	d.opt = 0
	if d.newer[d.c().ID] == als.Theirs {
		d.opt = 1
	}
}

func (d *conflictDialog) opts() []option {
	c, who := d.c(), d.r.ConflictWith.Name
	mine, theirs := "Keep mine", "Keep "+who+"'s"
	if d.newer != nil {
		if d.newer[c.ID] == als.Ours {
			mine += "  (newer)"
		} else {
			theirs += "  (newer)"
		}
	}
	o := []option{{label: mine}, {label: theirs}}
	if c.CanBoth {
		o = append(o, option{label: "Keep both", detail: "their clip goes to a free slot on the same track"})
	}
	return o
}

func (d *conflictDialog) form(a *App) form {
	c := d.c()
	who := d.r.ConflictWith.Name
	w := min(110, a.w-6)
	half := (w - 9) / 2
	left := preview(c.OursNode, c.Ours, half, 9)
	right := preview(c.TheirsNode, c.Theirs, half, 9)
	body := []string{
		sWarnText.Render(trunc(c.UnitLabel, w-6)),
		sDim.Render("You both changed this. Which version should stay?"), "",
		pad(sBold.Render("YOURS"), half) + "   " + sBold.Render(strings.ToUpper(who)+"'S"),
	}
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		body = append(body, pad(l, half)+" │ "+r)
	}
	f := form{title: fmt.Sprintf("Conflict with %s  (%d/%d)", who, d.cur+1, len(d.r.Conflicts)), width: w,
		body: body, options: d.opts(), cur: d.opt,
		hint: "enter choose · ←→ other conflicts · esc cancel (nothing changes)"}
	var done []string
	for i, x := range d.r.Conflicts {
		mark := "○"
		if r, ok := d.choice[x.ID]; ok {
			mark = "● " + map[als.Resolution]string{als.Ours: "mine", als.Theirs: who + "'s", als.Both: "both"}[r]
		}
		if i == d.cur {
			mark = "› " + mark
		} else {
			mark = "  " + mark
		}
		done = append(done, sDim.Render(trunc(mark+"  "+x.UnitLabel, w-6)))
	}
	if len(done) > 1 {
		f.foot = done
	}
	return f
}

func (d *conflictDialog) update(a *App, msg tea.Msg) (dialog, tea.Cmd) {
	if isEsc(msg) {
		a.say(lWarn, "Merge cancelled - nothing changed. Get changes again to choose.")
		return nil, nil
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "left":
			if d.cur > 0 {
				d.cur--
				d.reset()
			}
			return d, nil
		case "right":
			if d.cur < len(d.r.Conflicts)-1 {
				d.cur++
				d.reset()
			}
			return d, nil
		}
	}
	i := optionKeys(&d.opt, len(d.opts()), msg)
	if i < 0 {
		return d, nil
	}
	d.choice[d.c().ID] = []als.Resolution{als.Ours, als.Theirs, als.Both}[i]
	// nastepny bez wyboru albo koniec
	for j := range d.r.Conflicts {
		k := (d.cur + 1 + j) % len(d.r.Conflicts)
		if _, ok := d.choice[d.r.Conflicts[k].ID]; !ok {
			d.cur = k
			d.reset()
			return d, nil
		}
	}
	return d.done(a, d.choice)
}
